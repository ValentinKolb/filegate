package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigSetWritesYAMLBackupAndLoads(t *testing.T) {
	base := mustMkdir(t, t.TempDir(), "data")
	cfgPath := writeCLIConfig(t, base)

	out, err := executeCLI(
		"config", "--config", cfgPath, "set",
		"--server-listen", ":9091",
		"--s3-enabled",
		"--s3-access-key", "FGLEGACYACCESS",
		"--s3-secret-key", "legacy-secret",
		"--versioning-retention-bucket", "keep_for=2h,max_count=5",
	)
	if err != nil {
		t.Fatalf("config set: %v\n%s", err, out)
	}
	if !strings.Contains(out, "backup:") || !strings.Contains(out, "restart filegate") {
		t.Fatalf("output=%q, want backup and restart notice", out)
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load written config: %v", err)
	}
	if cfg.Server.Listen != ":9091" {
		t.Fatalf("server.listen=%q, want :9091", cfg.Server.Listen)
	}
	if !cfg.S3.Enabled || cfg.S3.AccessKey != "FGLEGACYACCESS" || cfg.S3.SecretKey != "legacy-secret" {
		t.Fatalf("s3 legacy fields not written: %+v", cfg.S3)
	}
	if len(cfg.Versioning.RetentionBuckets) != 1 {
		t.Fatalf("retention buckets len=%d, want 1", len(cfg.Versioning.RetentionBuckets))
	}
	if cfg.Versioning.RetentionBuckets[0].KeepFor != 2*time.Hour || cfg.Versioning.RetentionBuckets[0].MaxCount != 5 {
		t.Fatalf("retention bucket=%+v, want 2h/max 5", cfg.Versioning.RetentionBuckets[0])
	}
	if backups := matchingFiles(t, filepath.Dir(cfgPath), filepath.Base(cfgPath)+".bak."); len(backups) != 1 {
		t.Fatalf("backup count=%d, want 1 (%v)", len(backups), backups)
	}
}

func TestConfigS3KeyLifecycleIntegration(t *testing.T) {
	base := mustMkdir(t, t.TempDir(), "data")
	cfgPath := writeCLIConfig(t, base)

	out, err := executeCLI(
		"config", "--config", cfgPath, "s3", "key", "add",
		"--access-key", "FGTESTACCESS",
		"--secret-key", "test-secret",
		"--bucket", "data",
		"--requests-per-second", "10",
		"--burst", "20",
	)
	if err != nil {
		t.Fatalf("s3 key add: %v\n%s", err, out)
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load after add: %v", err)
	}
	if len(cfg.S3.Keys) != 1 {
		t.Fatalf("keys len=%d, want 1", len(cfg.S3.Keys))
	}
	key := cfg.S3.Keys[0]
	if key.AccessKey != "FGTESTACCESS" || key.SecretKey != "test-secret" || strings.Join(key.Buckets, ",") != "data" {
		t.Fatalf("key mismatch: %+v", key)
	}
	if key.RequestsPerSecond != 10 || key.Burst != 20 {
		t.Fatalf("rate limit mismatch: %+v", key)
	}

	if out, err = executeCLI("config", "--config", cfgPath, "s3", "key", "disable", "FGTESTACCESS"); err != nil {
		t.Fatalf("s3 key disable: %v\n%s", err, out)
	}
	cfg, err = loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load after disable: %v", err)
	}
	if len(cfg.S3.Keys) != 1 || len(cfg.S3.Keys[0].Buckets) != 0 {
		t.Fatalf("disabled key buckets=%v, want empty", cfg.S3.Keys)
	}

	if out, err = executeCLI("config", "--config", cfgPath, "s3", "key", "remove", "FGTESTACCESS"); err != nil {
		t.Fatalf("s3 key remove: %v\n%s", err, out)
	}
	cfg, err = loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load after remove: %v", err)
	}
	if len(cfg.S3.Keys) != 0 {
		t.Fatalf("keys len=%d, want 0", len(cfg.S3.Keys))
	}
}

func TestConfigMountLifecycleIntegration(t *testing.T) {
	root := t.TempDir()
	base := mustMkdir(t, root, "data")
	next := mustMkdir(t, root, "backup")
	cfgPath := writeCLIConfig(t, base)

	out, err := executeCLI("config", "--config", cfgPath, "mount", "add", next)
	if err != nil {
		if strings.Contains(err.Error(), "xattr") {
			t.Skipf("test filesystem does not support filegate xattr health probe: %v", err)
		}
		t.Fatalf("mount add: %v\n%s", err, out)
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load after mount add: %v", err)
	}
	if !containsString(cfg.Storage.BasePaths, next) {
		t.Fatalf("base paths=%v, want %s", cfg.Storage.BasePaths, next)
	}

	if out, err = executeCLI("config", "--config", cfgPath, "mount", "remove", "backup"); err != nil {
		t.Fatalf("mount remove: %v\n%s", err, out)
	}
	cfg, err = loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load after mount remove: %v", err)
	}
	if containsString(cfg.Storage.BasePaths, next) {
		t.Fatalf("base paths=%v still contains %s", cfg.Storage.BasePaths, next)
	}
}

func TestConfigMutationsRequireExplicitConfigFlag(t *testing.T) {
	base := mustMkdir(t, t.TempDir(), "data")
	cfgPath := writeCLIConfig(t, base)
	t.Setenv("FILEGATE_CONFIG", cfgPath)

	out, err := executeCLI("config", "set", "--server-listen", ":9092")
	if err == nil {
		t.Fatalf("config set without explicit --config succeeded: %s", out)
	}
	if !strings.Contains(err.Error(), "explicit --config") {
		t.Fatalf("error=%q, want explicit --config", err.Error())
	}
}

func TestConfigInvalidWriteKeepsOriginal(t *testing.T) {
	base := mustMkdir(t, t.TempDir(), "data")
	cfgPath := writeCLIConfig(t, base)

	out, err := executeCLI("config", "--config", cfgPath, "set", "--upload-max-chunked-upload-bytes", "1")
	if err == nil {
		t.Fatalf("invalid config set succeeded: %s", out)
	}
	cfg, loadErr := loadConfig(cfgPath)
	if loadErr != nil {
		t.Fatalf("load original config: %v", loadErr)
	}
	if cfg.Upload.MaxChunkedUploadBytes == 1 {
		t.Fatalf("invalid value was written")
	}
	if backups := matchingFiles(t, filepath.Dir(cfgPath), filepath.Base(cfgPath)+".bak."); len(backups) != 0 {
		t.Fatalf("backups=%v, want none before invalid replace", backups)
	}
}

func TestConfigSetCreatesUniqueBackups(t *testing.T) {
	base := mustMkdir(t, t.TempDir(), "data")
	cfgPath := writeCLIConfig(t, base)

	if out, err := executeCLI("config", "--config", cfgPath, "set", "--server-listen", ":9093"); err != nil {
		t.Fatalf("first config set: %v\n%s", err, out)
	}
	if out, err := executeCLI("config", "--config", cfgPath, "set", "--server-listen", ":9094"); err != nil {
		t.Fatalf("second config set: %v\n%s", err, out)
	}
	if backups := matchingFiles(t, filepath.Dir(cfgPath), filepath.Base(cfgPath)+".bak."); len(backups) != 2 {
		t.Fatalf("backup count=%d, want 2 (%v)", len(backups), backups)
	}
}

func TestConfigFlagsRegisteredOnServeAndSet(t *testing.T) {
	serve := newDaemonServeCmd()
	set := newConfigSetCmd(new(string))
	for _, spec := range allConfigFlagSpecs() {
		if serve.Flags().Lookup(spec.Name) == nil {
			t.Fatalf("serve missing config flag %s for %s", spec.Name, spec.Path)
		}
		if set.Flags().Lookup(spec.Name) == nil {
			t.Fatalf("config set missing config flag %s for %s", spec.Name, spec.Path)
		}
	}
}

func TestConfigShowRedactsSecrets(t *testing.T) {
	base := mustMkdir(t, t.TempDir(), "data")
	cfgPath := writeCLIConfig(t, base)
	out, err := executeCLI("config", "--config", cfgPath, "set", "--metrics-token", "scrape-secret")
	if err != nil {
		t.Fatalf("set metrics token: %v\n%s", err, out)
	}

	out, err = executeCLI("config", "--config", cfgPath, "show", "--format", "json")
	if err != nil {
		t.Fatalf("config show: %v\n%s", err, out)
	}
	if strings.Contains(out, "scrape-secret") {
		t.Fatalf("config show leaked secret: %s", out)
	}
	if !strings.Contains(out, "<redacted>") {
		t.Fatalf("config show did not redact secret: %s", out)
	}
}

func executeCLI(args ...string) (string, error) {
	cmd := NewRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func writeCLIConfig(t *testing.T, base string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "conf.yaml")
	content := "" +
		"auth:\n" +
		"  bearer_token: test-token\n" +
		"storage:\n" +
		"  base_paths:\n" +
		"    - " + base + "\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func mustMkdir(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

func matchingFiles(t *testing.T, dir, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var out []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			out = append(out, entry.Name())
		}
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
