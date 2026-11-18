package deploy

import (
	"strings"
	"testing"
)

func TestRestoreRemoteBackupCommand(t *testing.T) {
	cmd := restoreRemoteBackupCommand("/opt/app data", "/opt/app data.bak-20260101010101")

	for _, want := range []string{
		"mv '/opt/app data' '/opt/app data.failed-",
		"mv '/opt/app data.bak-20260101010101' '/opt/app data'",
		"rm -rf '/opt/app data.failed-",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("恢复命令缺少 %q，实际: %s", want, cmd)
		}
	}
}

func TestRestoreRemoteBackupCommandEscapesQuote(t *testing.T) {
	cmd := restoreRemoteBackupCommand("/opt/it's", "/opt/it's.bak")
	if !strings.Contains(cmd, `'/opt/it'\''s'`) {
		t.Fatalf("路径中的单引号未转义: %s", cmd)
	}
}
