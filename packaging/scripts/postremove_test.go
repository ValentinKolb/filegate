package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPostremoveRemovesManagedFgCommandLink(t *testing.T) {
	binDir := t.TempDir()
	link := filepath.Join(binDir, "fg")
	if err := os.Symlink("filegate", link); err != nil {
		t.Fatalf("symlink fg: %v", err)
	}

	out, err := runPostremove(t, binDir)
	if err != nil {
		t.Fatalf("postremove failed: %v\n%s", err, out)
	}

	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("fg link stat error=%v, want removed", err)
	}
}

func TestPostremoveKeepsUnmanagedFgLink(t *testing.T) {
	binDir := t.TempDir()
	link := filepath.Join(binDir, "fg")
	if err := os.Symlink("/custom/fg", link); err != nil {
		t.Fatalf("symlink fg: %v", err)
	}

	out, err := runPostremove(t, binDir)
	if err != nil {
		t.Fatalf("postremove failed: %v\n%s", err, out)
	}

	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink %s: %v", link, err)
	}
	if target != "/custom/fg" {
		t.Fatalf("fg link target=%q, want unmanaged link preserved", target)
	}
}

func TestPostremoveKeepsManagedFgCommandLinkOnUpgrade(t *testing.T) {
	binDir := t.TempDir()
	link := filepath.Join(binDir, "fg")
	if err := os.Symlink("filegate", link); err != nil {
		t.Fatalf("symlink fg: %v", err)
	}

	out, err := runPostremove(t, binDir, "upgrade")
	if err != nil {
		t.Fatalf("postremove failed: %v\n%s", err, out)
	}

	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink %s: %v", link, err)
	}
	if target != "filegate" {
		t.Fatalf("fg link target=%q, want managed link preserved on upgrade", target)
	}
}

func runPostremove(t *testing.T, binDir string, args ...string) (string, error) {
	t.Helper()

	stubDir := t.TempDir()
	writeExecutable(t, filepath.Join(stubDir, "systemctl"), "#!/bin/sh\nexit 0\n")

	cmdArgs := append([]string{"./postremove.sh"}, args...)
	cmd := exec.Command("sh", cmdArgs...)
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FILEGATE_BINDIR="+binDir,
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
