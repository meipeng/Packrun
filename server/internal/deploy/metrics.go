package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/packrun/packrun/internal/model"
)

// Prober 可选能力：采集服务器运行指标（负载/内存/磁盘/系统信息）。
// dirs 为该服务器已登记的部署目录，采集其所在文件系统占用。
type Prober interface {
	Probe(ctx context.Context, srv model.Server, dirs []string) (*model.ServerMetric, error)
}

// 采集字段行前缀，便于按行解析远端输出。
const (
	prHost  = "PRHOST="
	prOS    = "PROS="
	prUp    = "PRUP="
	prCores = "PRCORES="
	prLoad  = "PRLOAD="
	prMem   = "PRMEM="
	prDF    = "PRDF "
)

// Probe SSH 模式：一次远程 exec 采集全部指标。
func (d *SSHDeployer) Probe(ctx context.Context, srv model.Server, dirs []string) (*model.ServerMetric, error) {
	client, err := d.dial(srv)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	remote := &remoteExec{client: client, timeout: 30 * time.Second}
	out, err := remote.run(ctx, buildMetricsCommand(dirs))
	if err != nil {
		return nil, fmt.Errorf("执行采集命令失败: %v %s", err, out)
	}
	m, err := parseMetricsOutput(out, time.Now())
	if err != nil {
		return nil, err
	}
	// 解析函数只负责输出内容，必须在这里补上归属服务器，否则快照会写到 server_id=0。
	m.ServerID = srv.ID
	return m, nil
}

// Probe 本地沙箱模式不连真实服务器，返回在线占位快照。
func (d *LocalDeployer) Probe(ctx context.Context, srv model.Server, dirs []string) (*model.ServerMetric, error) {
	return &model.ServerMetric{
		ServerID:  srv.ID,
		Status:    "online",
		Message:   "本地沙箱模式，不采集真实运行指标",
		ProbeMode: "local",
		ProbedAt:  time.Now(),
	}, nil
}

var (
	_ Prober = (*SSHDeployer)(nil)
	_ Prober = (*LocalDeployer)(nil)
	_ Pinger = (*SSHDeployer)(nil)
	_ Pinger = (*LocalDeployer)(nil)
)

// buildMetricsCommand 生成一条 POSIX sh 采集命令：主机名、系统、运行时长、核数、
// 负载、内存与各目录磁盘占用。输出为带前缀的行，缺失字段留空，兼容无 /proc 的系统。
func buildMetricsCommand(dirs []string) string {
	var b strings.Builder
	b.WriteString(`echo "` + prHost + `$(hostname 2>/dev/null)"; `)
	b.WriteString(`echo "` + prOS + `$(uname -sr 2>/dev/null)"; `)
	b.WriteString(`echo "` + prUp + `$(cut -d ' ' -f1 /proc/uptime 2>/dev/null)"; `)
	b.WriteString(`echo "` + prCores + `$(nproc 2>/dev/null)"; `)
	b.WriteString(`echo "` + prLoad + `$(cat /proc/loadavg 2>/dev/null)"; `)
	b.WriteString(`echo "` + prMem + `$(awk '/^(MemTotal|MemAvailable):/{print $2}' /proc/meminfo 2>/dev/null | tr '\n' ' ')"; `)
	for _, p := range dedupePaths(append([]string{"/"}, dirs...)) {
		b.WriteString(`echo "` + prDF + p + `|$(df -kP ` + shellQuote(p) + ` 2>/dev/null | tail -1)"; `)
	}
	return b.String()
}

// dedupePaths 去重并保持顺序。
func dedupePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	var out []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// parseMetricsOutput 解析带前缀的采集输出为指标快照。
func parseMetricsOutput(out string, now time.Time) (*model.ServerMetric, error) {
	m := &model.ServerMetric{
		Status:    "online",
		ProbeMode: "ssh",
		ProbedAt:  now,
	}
	saw := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, prHost):
			m.Hostname = strings.TrimPrefix(line, prHost)
			saw = true
		case strings.HasPrefix(line, prOS):
			m.OS = strings.TrimPrefix(line, prOS)
			saw = true
		case strings.HasPrefix(line, prUp):
			fields := strings.Fields(strings.TrimPrefix(line, prUp))
			if len(fields) > 0 {
				if sec, err := strconv.ParseFloat(fields[0], 64); err == nil {
					m.UptimeSec = int64(sec)
				}
			}
			saw = true
		case strings.HasPrefix(line, prCores):
			if n, err := strconv.Atoi(strings.TrimPrefix(line, prCores)); err == nil && n > 0 {
				m.CPUCores = n
			}
			saw = true
		case strings.HasPrefix(line, prLoad):
			fields := strings.Fields(strings.TrimPrefix(line, prLoad))
			if len(fields) >= 3 {
				m.Load1, _ = strconv.ParseFloat(fields[0], 64)
				m.Load5, _ = strconv.ParseFloat(fields[1], 64)
				m.Load15, _ = strconv.ParseFloat(fields[2], 64)
			}
			saw = true
		case strings.HasPrefix(line, prMem):
			fields := strings.Fields(strings.TrimPrefix(line, prMem))
			if len(fields) >= 2 {
				totalKB, _ := strconv.Atoi(fields[0])
				availKB, _ := strconv.Atoi(fields[1])
				m.MemTotalMB = kbToMB(totalKB)
				m.MemUsedMB = kbToMB(totalKB - availKB)
			}
			saw = true
		case strings.HasPrefix(line, prDF):
			// 格式：PRDF <路径>|<df -kP 数据行>
			body := strings.TrimPrefix(line, prDF)
			idx := strings.Index(body, "|")
			if idx < 0 {
				continue
			}
			usage := parseDFRow(body[:idx], body[idx+1:])
			if usage != nil {
				m.DirUsage = appendJSONDirUsage(m.DirUsage, usage)
				saw = true
			}
		}
	}
	if !saw {
		return nil, fmt.Errorf("远程未返回可解析的指标数据")
	}
	return m, nil
}

// parseDFRow 解析 df -kP 数据行：<路径>, "文件系统 1024块 已用 可用 容量% 挂载点"。
func parseDFRow(path, row string) *model.DirUsage {
	fields := strings.Fields(row)
	if len(fields) < 5 {
		return nil
	}
	totalKB, err1 := strconv.Atoi(fields[1])
	usedKB, err2 := strconv.Atoi(fields[2])
	if err1 != nil || err2 != nil || totalKB <= 0 {
		return nil
	}
	pct := 0.0
	if p, err := strconv.ParseFloat(strings.TrimSuffix(fields[4], "%"), 64); err == nil {
		pct = p
	} else {
		pct = float64(usedKB) / float64(totalKB) * 100
	}
	return &model.DirUsage{
		Path:    path,
		TotalMB: kbToMB(totalKB),
		UsedMB:  kbToMB(usedKB),
		Pct:     pct,
	}
}

func kbToMB(kb int) int {
	if kb <= 0 {
		return 0
	}
	return (kb + 1023) / 1024
}

// appendJSONDirUsage 把一条目录占用追加到 JSON 数组文本（首次创建数组）。
func appendJSONDirUsage(existing string, usage *model.DirUsage) string {
	var list []model.DirUsage
	if existing != "" {
		_ = json.Unmarshal([]byte(existing), &list)
	}
	list = append(list, *usage)
	b, _ := json.Marshal(list)
	return string(b)
}
