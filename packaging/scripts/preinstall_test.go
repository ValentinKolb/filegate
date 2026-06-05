package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreinstallFailsWhenFilegateServiceIsActive(t *testing.T) {
	out, err := runPreinstallWithSystemctl(t, "active")
	if err == nil {
		t.Fatalf("preinstall succeeded with active service; output=%s", out)
	}
	if !strings.Contains(out, "filegate.service is currently running") {
		t.Fatalf("output=%q, want active-service warning", out)
	}
	if !strings.Contains(out, "systemctl stop filegate") {
		t.Fatalf("output=%q, want stop instruction", out)
	}
}

func TestPreinstallSucceedsWhenFilegateServiceIsInactive(t *testing.T) {
	out, err := runPreinstallWithSystemctl(t, "inactive")
	if err != nil {
		t.Fatalf("preinstall failed with inactive service: %v\n%s", err, out)
	}
}

func runPreinstallWithSystemctl(t *testing.T, state string) (string, error) {
	t.Helper()

	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "systemctl"), "#!/bin/sh\nif [ \"$1\" = \"is-active\" ]; then\n  if [ \""+state+"\" = \"active\" ]; then\n    exit 0\n  fi\n  exit 3\nfi\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "getent"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "id"), "#!/bin/sh\nexit 0\n")

	cmd := exec.Command("sh", "./preinstall.sh")
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
