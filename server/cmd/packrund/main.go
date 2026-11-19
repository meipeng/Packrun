package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/packrun/packrun/internal/api"
	"github.com/packrun/packrun/internal/config"
	"github.com/packrun/packrun/internal/deploy"
	"github.com/packrun/packrun/internal/notify"
	"github.com/packrun/packrun/internal/secret"
	"github.com/packrun/packrun/internal/store"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "backup" {
		runBackup(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "gc" {
		runGC(os.Args[2:])
		return
	}

	cfgPath := flag.String("config", "configs/config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("配置安全校验失败: %v", err)
	}

	db, err := store.Open(cfg)
	if err != nil {
		log.Fatalf("初始化存储失败: %v", err)
	}
	if err := store.Seed(db, cfg); err != nil {
		log.Fatalf("初始化基础数据失败: %v", err)
	}
	if n := api.RecoverInterruptedTasks(db); n > 0 {
		log.Printf("[recover] %d 个未完成任务已标记为中断失败", n)
	}

	delay := time.Duration(cfg.Deploy.StepDelayMS) * time.Millisecond
	var executor deploy.Deployer
	switch cfg.Deploy.Mode {
	case "ssh":
		executor = &deploy.SSHDeployer{
			StepDelay: delay,
			Insecure:  os.Getenv("PACKRUN_SSH_INSECURE") == "1",
		}
		log.Printf("执行器: SSH（业务服务器远程执行）")
	case "local":
		executor = &deploy.LocalDeployer{SandboxRoot: cfg.Storage.SandboxRoot, StepDelay: delay}
		log.Printf("执行器: 本地沙箱 %s（脚本仅回显，不实际执行）", cfg.Storage.SandboxRoot)
	default:
		log.Fatalf("未知的 deploy.mode: %s", cfg.Deploy.Mode)
	}

	// 主机指纹 TOFU：首次连接自动登记（装配层负责落库与审计），仅 SSH 执行器有真实主机。
	if sshExec, ok := executor.(*deploy.SSHDeployer); ok {
		sshExec.AutoTrust = cfg.Deploy.AutoTrustHostKey
		sshExec.OnTrust = api.TrustHostKeyFunc(db)
		// 「直接录入」凭据解密：主密钥来自配置/环境变量，不落库。
		sshExec.SecretOpener = func(cipherText string) (string, error) {
			key, err := cfg.UsableCredentialKey()
			if err != nil {
				return "", err
			}
			return secret.Open(key, cipherText)
		}
		if cfg.Deploy.AutoTrustHostKey {
			log.Printf("主机指纹: 首次连接自动信任（TOFU），服务器重建后请在服务器管理中清除指纹")
		} else {
			log.Printf("主机指纹: 未开启自动信任，未登记指纹的服务器回退中控机 known_hosts 校验")
		}
	}

	// 「直接录入」凭据能力提示：未配置主密钥时功能不可用（使用时报错），启动不受影响。
	if _, err := cfg.UsableCredentialKey(); err != nil {
		log.Printf("[credential] %v（存量 env:/file: 引用不受影响）", err)
	}

	gin.SetMode(cfg.Server.Mode)
	app := api.NewApp(cfg, db, executor)

	// 后台指标采集：定时拉取各服务器负载/内存/磁盘快照，仅在 SSH 模式下开启。
	if cfg.Deploy.Mode == "ssh" {
		api.StartMetricWorker(app, time.Duration(cfg.Deploy.MetricsIntervalSec)*time.Second)
	}
	// 仓库与历史数据维护：上传回收、残留临时文件、任务日志、任务与审计留存，
	// 其中包仓库版本回收每天执行一次。
	api.StartMaintenanceWorker(app, time.Hour)
	// 任务步骤正文按保留天数清理，避免数据库随时间无限增长。
	api.StartTaskLogRetentionWorker(app, cfg.Storage.TaskLogRetentionDays)
	// 后台巡检：磁盘余量、包仓库配额、失败任务、证书到期，按需推送告警。
	// 去重状态落库：重启后同一事件在窗口内不会重复推送。
	var notifier notify.Notifier = notify.Nop{}
	if url := strings.TrimSpace(cfg.Alert.WebhookURL); url != "" {
		notifier = notify.NewWebhook(url, cfg.Alert.Format, time.Duration(cfg.Alert.MinIntervalSec)*time.Second)
	}
	api.StartWatchdog(app, notifier, &api.GormDedupStore{DB: db}, time.Duration(cfg.Alert.CheckIntervalSec)*time.Second)

	handler := http.Handler(api.NewRouter(app))
	if cfg.Server.BasePath != "/" {
		handler = api.WithBasePath(handler, cfg.Server.BasePath)
	}

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// 单包上限 200MB，公网/隧道上传耗时可能远超 1 分钟，读超时需覆盖完整请求体；
		// 写超时比读超时更宽，避免上传刚结束、响应写入时刚好超时。
		ReadTimeout:    10 * time.Minute,
		WriteTimeout:   15 * time.Minute,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	log.Printf("Packrun 中控服务启动于 %s，部署路径 %s", cfg.Server.Addr, cfg.Server.BasePath)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务退出: %v", err)
	}
}

// runBackup 执行一次在线数据备份，供 cron/systemd timer 调用。
func runBackup(args []string) {
	fs := flag.NewFlagSet("packrund backup", flag.ExitOnError)
	cfgPath := fs.String("config", "configs/config.yaml", "配置文件路径")
	out := fs.String("out", "./backups", "备份输出目录")
	keep := fs.Int("keep", 7, "保留最近 N 份备份")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	db, err := store.OpenForBackup(cfg)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("获取数据库连接失败: %v", err)
	}
	defer sqlDB.Close()

	dest, err := store.Backup(db, cfg, *out, *keep)
	if err != nil {
		log.Fatalf("备份失败: %v", err)
	}
	fmt.Printf("备份完成: %s\n", dest)
}

// runGC 回收包仓库中的历史安装包。默认 dry-run：先看清单，确认后再加 -apply。
// 复用 OpenForBackup 打开数据库：不执行 AutoMigrate，避免维护命令顺手改线上表结构。
func runGC(args []string) {
	fs := flag.NewFlagSet("packrund gc", flag.ExitOnError)
	cfgPath := fs.String("config", "configs/config.yaml", "配置文件路径")
	apply := fs.Bool("apply", false, "真正删除文件与记录（默认只打印清单）")
	keep := fs.Int("keep", -1, "每个目录保留最近 N 个版本的包，-1 表示使用配置值")
	keepDays := fs.Int("keep-days", -1, "最近 N 天上传的包一律保留，-1 表示使用配置值")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if *keep < 0 {
		*keep = cfg.Storage.PackageKeepReleases
	}
	if *keepDays < 0 {
		*keepDays = cfg.Storage.PackageKeepDays
	}
	db, err := store.OpenForBackup(cfg)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("获取数据库连接失败: %v", err)
	}
	defer sqlDB.Close()

	dryRun := !*apply
	if dryRun {
		fmt.Println("dry-run 模式：只打印将回收的包，加 -apply 才会真正删除")
	}
	removed, freed, failed, err := api.RunPackageGC(db, cfg.Storage.PackageDir, *keep, *keepDays, dryRun)
	if err != nil {
		log.Fatalf("包仓库回收失败: %v", err)
	}
	action := "回收"
	if dryRun {
		action = "将回收"
	}
	fmt.Printf("%s %d 个安装包，释放 %d MB（失败 %d 个）\n", action, removed, freed>>20, failed)
	if dryRun && removed > 0 {
		fmt.Println("确认无误后执行：packrund gc -apply")
	}
}
