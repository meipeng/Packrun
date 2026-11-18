package deploy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/packrun/packrun/internal/model"
)

func TestParseMetricsOutput(t *testing.T) {
	out := strings.Join([]string{
		"PRHOST=web-01",
		"PROS=Linux 5.15.0 x86_64",
		"PRUP=123456.78 400000.00",
		"PRCORES=8",
		"PRLOAD=0.52 0.38 0.30 2/345 1234",
		"PRMEM=16384000 8000000 ",
		"PRDF /|/dev/sda1 40213312 16200000 21933312 43% /",
		"PRDF /opt/apps/web|/dev/sdb1 10485760 2000000 8285760 20% /opt/apps/web",
		"",
	}, "\n")

	m, err := parseMetricsOutput(out, time.Now())
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if m.Hostname != "web-01" {
		t.Errorf("Hostname = %q, 期望 web-01", m.Hostname)
	}
	if m.OS != "Linux 5.15.0 x86_64" {
		t.Errorf("OS = %q", m.OS)
	}
	if m.UptimeSec != 123456 {
		t.Errorf("UptimeSec = %d, 期望 123456", m.UptimeSec)
	}
	if m.CPUCores != 8 {
		t.Errorf("CPUCores = %d, 期望 8", m.CPUCores)
	}
	if m.Load1 != 0.52 || m.Load5 != 0.38 || m.Load15 != 0.30 {
		t.Errorf("Load = %v/%v/%v, 期望 0.52/0.38/0.30", m.Load1, m.Load5, m.Load15)
	}
	if m.MemTotalMB != kbToMB(16384000) {
		t.Errorf("MemTotalMB = %d, 期望 %d", m.MemTotalMB, kbToMB(16384000))
	}
	if m.MemUsedMB != kbToMB(16384000-8000000) {
		t.Errorf("MemUsedMB = %d, 期望 %d", m.MemUsedMB, kbToMB(16384000-8000000))
	}
	if m.Status != "online" || m.ProbeMode != "ssh" {
		t.Errorf("Status/ProbeMode = %s/%s", m.Status, m.ProbeMode)
	}

	var usages []model.DirUsage
	if err := json.Unmarshal([]byte(m.DirUsage), &usages); err != nil {
		t.Fatalf("DirUsage 解析失败: %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("DirUsage 条数 = %d, 期望 2", len(usages))
	}
	if usages[0].Path != "/" || usages[0].Pct != 43 {
		t.Errorf("DirUsage[0] = %+v, 期望 / 43%%", usages[0])
	}
	if usages[1].Path != "/opt/apps/web" || usages[1].Pct != 20 {
		t.Errorf("DirUsage[1] = %+v, 期望 /opt/apps/web 20%%", usages[1])
	}
}

func TestParseMetricsOutputPartial(t *testing.T) {
	// 无 /proc 的系统：只回主机名与磁盘也应成功，缺失字段保持零值。
	out := "PRHOST=db-01\nPRDF /|/dev/sda1 10240 5120 5120 50% /\n"
	m, err := parseMetricsOutput(out, time.Now())
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if m.Hostname != "db-01" || m.UptimeSec != 0 || m.CPUCores != 0 {
		t.Errorf("部分字段解析不符: %+v", m)
	}
}

func TestParseMetricsOutputEmpty(t *testing.T) {
	if _, err := parseMetricsOutput("", time.Now()); err == nil {
		t.Fatal("空输出应返回错误")
	}
}

func TestBuildMetricsCommand(t *testing.T) {
	cmd := buildMetricsCommand([]string{"/opt/apps/a", "/opt/apps/a", "  ", "/opt/apps/b"})
	if got := strings.Count(cmd, prDF); got != 3 {
		t.Errorf("PRDF 条数 = %d, 期望 3（根目录 + 两个去重后的目录）", got)
	}
	if !strings.Contains(cmd, shellQuote("/opt/apps/a")) {
		t.Errorf("目录路径未做 shell 转义: %s", cmd)
	}
	if !strings.Contains(cmd, "df -kP '/'") {
		t.Errorf("缺少根目录磁盘采集: %s", cmd)
	}
}

func TestKBToMB(t *testing.T) {
	cases := []struct{ kb, want int }{
		{0, 0}, {-5, 0}, {1, 1}, {1024, 1}, {1025, 2}, {16384000, 16000},
	}
	for _, c := range cases {
		if got := kbToMB(c.kb); got != c.want {
			t.Errorf("kbToMB(%d) = %d, 期望 %d", c.kb, got, c.want)
		}
	}
}
