package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigJobDefaults(t *testing.T) {
	base := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	content := "auth:\n  bearer_token: test-token\nstorage:\n  base_paths:\n    - " + base + "\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Jobs.Workers <= 0 {
		t.Fatalf("jobs.workers must be > 0")
	}
	if cfg.Jobs.QueueSize != 8192 {
		t.Fatalf("jobs.queue_size=%d, want 8192", cfg.Jobs.QueueSize)
	}
	if cfg.Upload.MaxChunkedUploadBytes != int64(50*1024*1024*1024) {
		t.Fatalf("upload.max_chunked_upload_bytes=%d, want %d",
			cfg.Upload.MaxChunkedUploadBytes, int64(50*1024*1024*1024))
	}
	if cfg.Upload.MaxConcurrentChunkWrites <= 0 {
		t.Fatalf("upload.max_concurrent_chunk_writes=%d, want > 0", cfg.Upload.MaxConcurrentChunkWrites)
	}
	if cfg.Upload.MinFreeBytes != int64(64*1024*1024) {
		t.Fatalf("upload.min_free_bytes=%d, want %d", cfg.Upload.MinFreeBytes, int64(64*1024*1024))
	}
	if cfg.Thumbnail.MaxSourceBytes != int64(64*1024*1024) {
		t.Fatalf("thumbnail.max_source_bytes=%d, want %d",
			cfg.Thumbnail.MaxSourceBytes, int64(64*1024*1024))
	}
	if cfg.Thumbnail.MaxPixels != int64(40*1024*1024) {
		t.Fatalf("thumbnail.max_pixels=%d, want %d",
			cfg.Thumbnail.MaxPixels, int64(40*1024*1024))
	}
}

func TestLoadConfigJobOverrides(t *testing.T) {
	base := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	content := "" +
		"auth:\n" +
		"  bearer_token: test-token\n" +
		"storage:\n" +
		"  base_paths:\n" +
		"    - " + base + "\n" +
		"jobs:\n" +
		"  workers: 40\n" +
		"  queue_size: 12000\n" +
		"  thumbnail_workers: 90\n" +
		"  thumbnail_queue_size: 32000\n" +
		"upload:\n" +
		"  max_concurrent_chunk_writes: 77\n" +
		"  min_free_bytes: 123456\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Jobs.Workers != 40 || cfg.Jobs.QueueSize != 12000 {
		t.Fatalf("global jobs mismatch: workers=%d queue=%d", cfg.Jobs.Workers, cfg.Jobs.QueueSize)
	}
	if cfg.Jobs.ThumbnailWorkers != 90 || cfg.Jobs.ThumbnailQueueSize != 32000 {
		t.Fatalf("thumbnail jobs mismatch: workers=%d queue=%d", cfg.Jobs.ThumbnailWorkers, cfg.Jobs.ThumbnailQueueSize)
	}
	if cfg.Upload.MaxConcurrentChunkWrites != 77 {
		t.Fatalf("upload.max_concurrent_chunk_writes=%d, want 77", cfg.Upload.MaxConcurrentChunkWrites)
	}
	if cfg.Upload.MinFreeBytes != 123456 {
		t.Fatalf("upload.min_free_bytes=%d, want 123456", cfg.Upload.MinFreeBytes)
	}
}

func TestLoadConfigExplicitMissingFileReturnsError(t *testing.T) {
	_, err := loadConfig(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatalf("expected error for missing explicit config file")
	}
}

func TestLoadConfigServerWriteTimeoutDefaultAndOverride(t *testing.T) {
	base := t.TempDir()

	defaultCfgPath := filepath.Join(t.TempDir(), "config-default.yaml")
	defaultContent := "auth:\n  bearer_token: test-token\nstorage:\n  base_paths:\n    - " + base + "\n"
	if err := os.WriteFile(defaultCfgPath, []byte(defaultContent), 0o644); err != nil {
		t.Fatalf("write default config: %v", err)
	}
	defaultCfg, err := loadConfig(defaultCfgPath)
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	if defaultCfg.Server.WriteTimeout != 5*time.Minute {
		t.Fatalf("default write_timeout=%s, want 5m", defaultCfg.Server.WriteTimeout)
	}

	overrideCfgPath := filepath.Join(t.TempDir(), "config-override.yaml")
	overrideContent := "" +
		"server:\n" +
		"  write_timeout: 90s\n" +
		"auth:\n" +
		"  bearer_token: test-token\n" +
		"storage:\n" +
		"  base_paths:\n" +
		"    - " + base + "\n"
	if err := os.WriteFile(overrideCfgPath, []byte(overrideContent), 0o644); err != nil {
		t.Fatalf("write override config: %v", err)
	}
	overrideCfg, err := loadConfig(overrideCfgPath)
	if err != nil {
		t.Fatalf("load override config: %v", err)
	}
	if overrideCfg.Server.WriteTimeout != 90*time.Second {
		t.Fatalf("override write_timeout=%s, want 90s", overrideCfg.Server.WriteTimeout)
	}
}

func TestLoadConfigUsesFilegateConfigEnv(t *testing.T) {
	base := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "custom-conf.yaml")
	content := "" +
		"server:\n" +
		"  listen: \":9090\"\n" +
		"auth:\n" +
		"  bearer_token: test-token\n" +
		"storage:\n" +
		"  base_paths:\n" +
		"    - " + base + "\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("FILEGATE_CONFIG", cfgPath)
	cfg, err := loadConfig("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Server.Listen != ":9090" {
		t.Fatalf("listen=%q, want :9090", cfg.Server.Listen)
	}
}

func TestLoadConfigFindsConfYAMLInWorkingDir(t *testing.T) {
	base := t.TempDir()
	workDir := t.TempDir()
	confPath := filepath.Join(workDir, "conf.yaml")
	content := "" +
		"server:\n" +
		"  listen: \":9191\"\n" +
		"auth:\n" +
		"  bearer_token: test-token\n" +
		"storage:\n" +
		"  base_paths:\n" +
		"    - " + base + "\n"
	if err := os.WriteFile(confPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWD) }()

	cfg, err := loadConfig("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Server.Listen != ":9191" {
		t.Fatalf("listen=%q, want :9191", cfg.Server.Listen)
	}
}
