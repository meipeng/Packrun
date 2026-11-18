package api

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/deploy"
	"github.com/packrun/packrun/internal/model"
	"github.com/packrun/packrun/internal/secret"
)

// ---------- 服务器 ----------

// maskServerCredential 对非管理员隐藏凭据引用，管理员保留原值。
func maskServerCredential(u *model.User, srv *model.Server) {
	if u != nil && u.Role == model.RoleAdmin {
		return
	}
	srv.CredentialRef = "***"
}

// attachServerMetrics 批量填充服务器的最新运行指标快照。
func (a *App) attachServerMetrics(list []model.Server) {
	if len(list) == 0 {
		return
	}
	ids := make([]uint, 0, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
	}
	var metrics []model.ServerMetric
	if err := a.DB.Where("server_id IN ?", ids).Find(&metrics).Error; err != nil {
		return
	}
	byID := make(map[uint]*model.ServerMetric, len(metrics))
	for i := range metrics {
		byID[metrics[i].ServerID] = &metrics[i]
	}
	for i := range list {
		list[i].Metrics = byID[list[i].ID]
	}
}

func (a *App) listServers(c *gin.Context) {
	u := currentUser(c)
	var list []model.Server
	if err := applyServerScope(a.DB.Order("env asc, id asc"), u, "id").Find(&list).Error; err != nil {
		internal(c, "查询服务器失败")
		return
	}
	a.attachServerMetrics(list)
	for i := range list {
		maskServerCredential(u, &list[i])
	}
	ok(c, list)
}

func (a *App) getServer(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var srv model.Server
	if err := a.DB.First(&srv, id).Error; err != nil {
		notFound(c, "服务器不存在")
		return
	}
	u := currentUser(c)
	if !checkServerScope(u, srv.ID) {
		forbidden(c, "无权查看该服务器")
		return
	}
	one := []model.Server{srv}
	a.attachServerMetrics(one)
	srv = one[0]
	maskServerCredential(u, &srv)
	ok(c, srv)
}

type serverReq struct {
	Name          string `json:"name"`
	Env           string `json:"env"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	AuthType      string `json:"authType"`
	CredentialRef string `json:"credentialRef"`
	// CredentialSecret 「直接录入」的凭据明文：仅请求入参，入库前加密为 SecretCipher，永不回显。
	CredentialSecret string `json:"credentialSecret"`
	MaxConcurrent    int    `json:"maxConcurrent"`
	Remark           string `json:"remark"`
}

func (r serverReq) validate() string {
	if strings.TrimSpace(r.Name) == "" {
		return "服务器名称不能为空"
	}
	if err := deploy.ValidateServerName(strings.TrimSpace(r.Name)); err != nil {
		return err.Error()
	}
	if strings.TrimSpace(r.Host) == "" {
		return "主机地址不能为空"
	}
	switch r.Env {
	case model.EnvDev, model.EnvTest, model.EnvProd:
	default:
		return "环境只能是 dev / test / prod"
	}
	if r.AuthType != "key" && r.AuthType != "password" {
		return "认证方式只能是 key 或 password"
	}
	if r.CredentialSecret != "" {
		// 直接录入：明文走加密通道，引用字段后端自行置为内置标记。
		return ""
	}
	if r.CredentialRef == "" {
		return "凭据不能为空：请填写引用（env:/file:）或直接录入凭据内容"
	}
	if !strings.HasPrefix(r.CredentialRef, "env:") &&
		!strings.HasPrefix(r.CredentialRef, "file:") &&
		!deploy.IsBuiltInCredential(r.CredentialRef) {
		return "凭据必须使用引用（env:NAME 或 file:路径）、内置标记（secret:）或直接录入"
	}
	return ""
}

// sealDirectCredential 把「直接录入」的明文凭据加密为落库密文，返回内置引用标记 + 密文。
func (a *App) sealDirectCredential(plaintext string) (string, string, error) {
	key, err := a.Cfg.UsableCredentialKey()
	if err != nil {
		return "", "", err
	}
	sealed, err := secret.Seal(key, plaintext)
	if err != nil {
		return "", "", err
	}
	return deploy.CredentialRefBuiltIn, sealed, nil
}

func (a *App) createServer(c *gin.Context) {
	var req serverReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "参数格式错误")
		return
	}
	if msg := req.validate(); msg != "" {
		badRequest(c, msg)
		return
	}
	if req.Port <= 0 {
		req.Port = 22
	}
	if req.MaxConcurrent <= 0 {
		req.MaxConcurrent = 1
	}

	// 新增时不接受 secret: 标记（无既有密文可保持），必须引用或直接录入。
	credRef := req.CredentialRef
	credCipher := ""
	if req.CredentialSecret != "" {
		ref, sealed, err := a.sealDirectCredential(req.CredentialSecret)
		if err != nil {
			badRequest(c, err.Error())
			return
		}
		credRef, credCipher = ref, sealed
	} else if deploy.IsBuiltInCredential(credRef) {
		badRequest(c, "新增服务器请使用引用（env:/file:）或直接录入凭据，secret: 标记仅用于更新时保持内置不变")
		return
	}

	srv := model.Server{
		Name: req.Name, Env: req.Env, Host: req.Host, Port: req.Port,
		Username: req.Username, AuthType: req.AuthType, CredentialRef: credRef,
		MaxConcurrent: req.MaxConcurrent, Remark: req.Remark, Status: "unknown",
	}
	if credCipher != "" {
		srv.SecretCipher = credCipher
	}
	if err := a.DB.Create(&srv).Error; err != nil {
		fail(c, 409, CodeConflict, "服务器名称已存在")
		return
	}
	detail := "新增目标服务器"
	if credCipher != "" {
		detail += "（凭据为直接录入，已加密保存）"
	}
	a.audit(c, "server.create", srv.Name, "ok", "", detail, false)
	ok(c, srv)
}

func (a *App) updateServer(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var srv model.Server
	if err := a.DB.First(&srv, id).Error; err != nil {
		notFound(c, "服务器不存在")
		return
	}
	var req serverReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "参数格式错误")
		return
	}
	if msg := req.validate(); msg != "" {
		badRequest(c, msg)
		return
	}
	if req.Port <= 0 {
		req.Port = 22
	}
	if req.MaxConcurrent <= 0 {
		req.MaxConcurrent = 1
	}

	credRef := req.CredentialRef
	updates := map[string]any{
		"name": req.Name, "env": req.Env, "host": req.Host, "port": req.Port,
		"username": req.Username, "auth_type": req.AuthType,
		"max_concurrent": req.MaxConcurrent, "remark": req.Remark,
	}
	detail := "修改服务器配置"
	switch {
	case req.CredentialSecret != "":
		// 替换为新的直接录入凭据
		ref, sealed, err := a.sealDirectCredential(req.CredentialSecret)
		if err != nil {
			badRequest(c, err.Error())
			return
		}
		credRef = ref
		updates["secret_cipher"] = sealed
		detail = "修改服务器配置（凭据已更新为内置加密存储）"
	case deploy.IsBuiltInCredential(req.CredentialRef):
		// 保持内置不变：引用沿用库中原值，密文不动
		credRef = srv.CredentialRef
	default:
		// 换回引用方式：清理残留密文
		updates["secret_cipher"] = ""
	}
	updates["credential_ref"] = credRef

	a.DB.Model(&srv).Updates(updates)
	a.audit(c, "server.update", srv.Name, "ok", "", detail, false)
	ok(c, srv)
}

func (a *App) deleteServer(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var srv model.Server
	if err := a.DB.First(&srv, id).Error; err != nil {
		notFound(c, "服务器不存在")
		return
	}
	a.DB.Where("server_id = ?", srv.ID).Delete(&model.Directory{})
	a.DB.Delete(&srv)
	a.audit(c, "server.delete", srv.Name, "ok", "", "删除服务器及其目录配置", false)
	ok(c, gin.H{"deleted": true})
}

func (a *App) pingServer(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var srv model.Server
	if err := a.DB.First(&srv, id).Error; err != nil {
		notFound(c, "服务器不存在")
		return
	}
	if !checkServerScope(currentUser(c), srv.ID) {
		forbidden(c, "无权检测该服务器")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	pinger, okPing := a.Deployer.(deploy.Pinger)
	if !okPing {
		badRequest(c, "当前执行器不支持连通性检测")
		return
	}
	err := pinger.Ping(ctx, srv)
	status := "online"
	msg := "连接正常"
	probeMode := a.Cfg.Deploy.Mode
	if err != nil {
		status = "offline"
		msg = err.Error()
	}
	a.DB.Model(&srv).Update("status", status)
	a.audit(c, "server.ping", srv.Name, map[bool]string{true: "ok", false: "fail"}[err == nil], "", msg, false)
	ok(c, gin.H{"status": status, "message": msg, "mode": probeMode})
}

// probeServer 即时采集一次运行指标：负载/内存/磁盘/系统信息 + 凭据自检。
func (a *App) probeServer(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var srv model.Server
	if err := a.DB.First(&srv, id).Error; err != nil {
		notFound(c, "服务器不存在")
		return
	}
	if !checkServerScope(currentUser(c), srv.ID) {
		forbidden(c, "无权检测该服务器")
		return
	}
	prober, okProber := a.Deployer.(deploy.Prober)
	if !okProber {
		badRequest(c, "当前执行器不支持指标采集")
		return
	}

	var dirs []string
	a.DB.Model(&model.Directory{}).Where("server_id = ?", srv.ID).Pluck("path", &dirs)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	metric, err := prober.Probe(ctx, srv, dirs)

	status := "online"
	msg := "连接正常"
	if err != nil {
		status = "offline"
		msg = err.Error()
	}
	a.DB.Model(&srv).Update("status", status)

	// 成败都留一条快照，便于前端展示离线原因与采集时间。
	if metric == nil {
		metric = &model.ServerMetric{
			ServerID: srv.ID, Status: status, Message: msg,
			ProbeMode: a.Cfg.Deploy.Mode, ProbedAt: time.Now(),
		}
	}
	upsertServerMetric(a.DB, metric)

	// 凭据自检不阻断主流程，仅作为诊断信息返回；内置凭据需要执行器具备解密能力。
	resolver, _ := a.Deployer.(deploy.SecretResolver)
	credMsg, credErr := deploy.InspectCredential(srv, resolver)
	credential := gin.H{"ok": credErr == nil}
	if credErr != nil {
		credential["message"] = credErr.Error()
	} else {
		credential["message"] = credMsg
	}

	a.audit(c, "server.probe", srv.Name, map[bool]string{true: "ok", false: "fail"}[err == nil], "", msg, false)
	ok(c, gin.H{
		"status": status, "message": msg, "metric": metric,
		"credential": credential, "mode": a.Cfg.Deploy.Mode,
	})
}

// probeServerHostKey 采集目标机当前主机指纹（不改变登记状态）。
func (a *App) probeServerHostKey(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var srv model.Server
	if err := a.DB.First(&srv, id).Error; err != nil {
		notFound(c, "服务器不存在")
		return
	}
	if !checkServerScope(currentUser(c), srv.ID) {
		forbidden(c, "无权采集该服务器指纹")
		return
	}
	sshExec, okSSH := a.Deployer.(*deploy.SSHDeployer)
	if !okSSH {
		badRequest(c, "当前执行器不支持指纹采集（本地沙箱模式无真实主机）")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	info, err := sshExec.ProbeHostKey(ctx, srv)
	if err != nil {
		a.audit(c, "server.hostkey.probe", srv.Name, "fail", "", err.Error(), false)
		badRequest(c, err.Error())
		return
	}
	detail := fmt.Sprintf("指纹 %s（%s）", info.Fingerprint, info.Algo)
	if info.Pinned && !info.Matched {
		detail = "指纹不匹配：" + detail
	}
	a.audit(c, "server.hostkey.probe", srv.Name, "ok", "", detail, false)
	ok(c, info)
}

// clearServerHostKey 清除已登记的主机指纹，下次连接重新 TOFU 信任（服务器重建场景）。
func (a *App) clearServerHostKey(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var srv model.Server
	if err := a.DB.First(&srv, id).Error; err != nil {
		notFound(c, "服务器不存在")
		return
	}
	a.DB.Model(&srv).Updates(map[string]any{
		"host_key_algo": "", "host_key": "", "host_key_fingerprint": "", "host_key_pinned_at": nil,
	})
	a.audit(c, "server.hostkey.clear", srv.Name, "ok", "", "清除主机指纹，下次连接将重新自动信任", false)
	ok(c, gin.H{"cleared": true})
}

// TrustHostKeyFunc TOFU 首次信任的落库钩子：仅在服务器尚未登记指纹时写入，
// 并记录一条系统审计日志。并发连接下靠 WHERE 守卫保证只写一次。
func TrustHostKeyFunc(db *gorm.DB) deploy.HostKeyTrust {
	return func(srv model.Server, algo, fingerprint, keyBase64 string) {
		now := time.Now()
		res := db.Model(&model.Server{}).
			Where("id = ? AND (host_key_fingerprint IS NULL OR host_key_fingerprint = '')", srv.ID).
			Updates(map[string]any{
				"host_key_algo": algo, "host_key": keyBase64,
				"host_key_fingerprint": fingerprint, "host_key_pinned_at": now,
			})
		if res.Error != nil || res.RowsAffected == 0 {
			return
		}
		db.Create(&model.AuditLog{
			Actor:  "system",
			Action: "server.hostkey.autotrust",
			Target: srv.Name,
			Result: "ok",
			Detail: fmt.Sprintf("首次连接，自动登记主机指纹 %s（%s）", fingerprint, algo),
		})
		log.Printf("[hostkey] 已为服务器 %s 自动登记主机指纹 %s（%s）", srv.Name, fingerprint, algo)
	}
}

// upsertServerMetric 写入服务器最新指标快照（每台服务器一行）。
func upsertServerMetric(db *gorm.DB, m *model.ServerMetric) {
	var existing model.ServerMetric
	if err := db.Where("server_id = ?", m.ServerID).First(&existing).Error; err == nil {
		m.ID = existing.ID
		db.Save(m)
		return
	}
	db.Create(m)
}

// ---------- 部署目录 ----------

func (a *App) listDirectories(c *gin.Context) {
	u := currentUser(c)
	serverID, _ := strconv.Atoi(c.Query("serverId"))
	q := a.DB.Order("id asc")
	if serverID > 0 {
		if !checkServerScope(u, uint(serverID)) {
			forbidden(c, "无权查看该服务器的目录")
			return
		}
		q = q.Where("server_id = ?", serverID)
	}
	q = applyServerScope(q, u, "server_id")
	var list []model.Directory
	if err := q.Find(&list).Error; err != nil {
		internal(c, "查询目录失败")
		return
	}
	ok(c, list)
}

type directoryReq struct {
	ServerID           uint   `json:"serverId"`
	Path               string `json:"path"`
	Owner              string `json:"owner"`
	AllowOverwrite     bool   `json:"allowOverwrite"`
	BackupBeforeDeploy bool   `json:"backupBeforeDeploy"`
	PackageMode        string `json:"packageMode"`
	KeepReleases       int    `json:"keepReleases"`
	MaxSizeMB          int    `json:"maxSizeMB"`
	Remark             string `json:"remark"`
}

// normalizePackageMode 空值按自动解压处理，非法值返回 false。
func normalizePackageMode(mode string) (string, bool) {
	switch strings.TrimSpace(mode) {
	case "", model.PackageModeUnpack:
		return model.PackageModeUnpack, true
	case model.PackageModeUpload:
		return model.PackageModeUpload, true
	default:
		return "", false
	}
}

func (a *App) createDirectory(c *gin.Context) {
	var req directoryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "参数格式错误")
		return
	}
	var srv model.Server
	if err := a.DB.First(&srv, req.ServerID).Error; err != nil {
		badRequest(c, "目标服务器不存在")
		return
	}
	path, err := deploy.NormalizePath(req.Path)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	var exists int64
	a.DB.Model(&model.Directory{}).Where("server_id = ? AND path = ?", req.ServerID, path).Count(&exists)
	if exists > 0 {
		fail(c, 409, CodeConflict, "该服务器下已存在相同目录")
		return
	}
	if req.MaxSizeMB <= 0 {
		req.MaxSizeMB = 1024
	}
	packageMode, valid := normalizePackageMode(req.PackageMode)
	if !valid {
		badRequest(c, "包处理方式非法")
		return
	}
	dir := model.Directory{
		ServerID: req.ServerID, Path: path, Owner: req.Owner,
		AllowOverwrite: req.AllowOverwrite, BackupBeforeDeploy: req.BackupBeforeDeploy,
		PackageMode:  packageMode,
		KeepReleases: req.KeepReleases, MaxSizeMB: req.MaxSizeMB, Remark: req.Remark,
	}
	if err := a.DB.Create(&dir).Error; err != nil {
		internal(c, "创建目录配置失败")
		return
	}
	a.audit(c, "directory.create", srv.Name+":"+path, "ok", "", "新增部署目录", false)
	ok(c, dir)
}

func (a *App) updateDirectory(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var dir model.Directory
	if err := a.DB.First(&dir, id).Error; err != nil {
		notFound(c, "目录配置不存在")
		return
	}
	var req directoryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "参数格式错误")
		return
	}
	path, err := deploy.NormalizePath(req.Path)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	updates := map[string]any{
		"path": path, "owner": req.Owner, "allow_overwrite": req.AllowOverwrite,
		"backup_before_deploy": req.BackupBeforeDeploy, "keep_releases": req.KeepReleases,
		"max_size_mb": req.MaxSizeMB, "remark": req.Remark,
	}
	// 旧客户端不提交 packageMode 时保持原值，避免误重置。
	if strings.TrimSpace(req.PackageMode) != "" {
		packageMode, valid := normalizePackageMode(req.PackageMode)
		if !valid {
			badRequest(c, "包处理方式非法")
			return
		}
		updates["package_mode"] = packageMode
	}
	a.DB.Model(&dir).Updates(updates)
	// 回读，确保响应包含更新后的路径与包处理方式。
	if err := a.DB.First(&dir, id).Error; err != nil {
		internal(c, "读取目录配置失败")
		return
	}
	a.audit(c, "directory.update", path, "ok", "", "修改部署目录策略", false)
	ok(c, dir)
}

func (a *App) deleteDirectory(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var dir model.Directory
	if err := a.DB.First(&dir, id).Error; err != nil {
		notFound(c, "目录配置不存在")
		return
	}
	a.DB.Delete(&dir)
	a.audit(c, "directory.delete", dir.Path, "ok", "", "删除部署目录配置", false)
	ok(c, gin.H{"deleted": true})
}
