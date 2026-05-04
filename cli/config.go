package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/valentinkolb/filegate/domain"
)

func loadConfig(configFile string) (domain.Config, error) {
	v := viper.New()

	v.SetDefault("server.listen", ":8080")
	v.SetDefault("server.write_timeout", "5m")
	v.SetDefault("server.access_log_enabled", true)
	v.SetDefault("auth.bearer_token", "")
	v.SetDefault("storage.base_paths", []string{})
	v.SetDefault("storage.index_path", "/var/lib/filegate/index")
	v.SetDefault("detection.backend", "auto")
	v.SetDefault("detection.poll_interval", "3s")
	v.SetDefault("cache.path_cache_size", 100000)
	v.SetDefault("jobs.workers", defaultJobWorkers())
	v.SetDefault("jobs.queue_size", 8192)
	v.SetDefault("jobs.thumbnail_workers", 0)
	v.SetDefault("jobs.thumbnail_queue_size", 0)
	v.SetDefault("upload.expiry", "24h")
	v.SetDefault("upload.cleanup_interval", "6h")
	v.SetDefault("upload.max_chunk_bytes", int64(50*1024*1024))
	v.SetDefault("upload.max_upload_bytes", int64(500*1024*1024))
	v.SetDefault("upload.max_chunked_upload_bytes", int64(50*1024*1024*1024))
	v.SetDefault("upload.max_concurrent_chunk_writes", defaultChunkWriteConcurrency())
	v.SetDefault("upload.min_free_bytes", int64(64*1024*1024))
	v.SetDefault("thumbnail.lru_cache_size", 1024)
	v.SetDefault("thumbnail.max_source_bytes", int64(64*1024*1024))
	v.SetDefault("thumbnail.max_pixels", int64(40*1024*1024))
	v.SetDefault("versioning.enabled", "auto")
	v.SetDefault("versioning.cooldown", "15m")
	v.SetDefault("versioning.min_size_for_auto_v1", int64(64*1024))
	v.SetDefault("versioning.pruner_interval", "5m")
	v.SetDefault("versioning.max_pinned_per_file", 100)
	v.SetDefault("versioning.pinned_grace_after_delete", "720h") // 30 days
	v.SetDefault("versioning.max_label_bytes", 2048)
	// retention_buckets defaults to a sane bucketed exponential-decay
	// schedule. Without this, an operator who enables versioning but
	// forgets to define buckets would silently accumulate versions
	// forever (every write captures a new one). Operators can disable
	// retention by passing an explicitly empty list AND understanding
	// the storage-growth implications.
	v.SetDefault("versioning.retention_buckets", []map[string]any{
		{"keep_for": "1h", "max_count": -1}, // all in last hour
		{"keep_for": "24h", "max_count": 24},
		{"keep_for": "720h", "max_count": 30},  // ~daily for last 30d
		{"keep_for": "8760h", "max_count": 12}, // ~monthly for last 1y
	})

	configFile = strings.TrimSpace(configFile)
	if configFile == "" {
		configFile = strings.TrimSpace(os.Getenv("FILEGATE_CONFIG"))
	}

	if configFile != "" {
		v.SetConfigFile(configFile)
		if err := v.ReadInConfig(); err != nil {
			return domain.Config{}, err
		}
	} else {
		for _, candidate := range defaultConfigCandidates() {
			if _, err := os.Stat(candidate); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return domain.Config{}, err
			}
			v.SetConfigFile(candidate)
			if err := v.ReadInConfig(); err != nil {
				return domain.Config{}, err
			}
			break
		}
	}

	v.SetEnvPrefix("FILEGATE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	var cfg domain.Config
	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, err
	}

	// Accept base paths from env as comma or semicolon separated list.
	if envBase := strings.TrimSpace(v.GetString("storage.base_paths")); envBase != "" {
		if len(cfg.Storage.BasePaths) == 0 {
			cfg.Storage.BasePaths = splitList(envBase)
		}
	}
	if len(cfg.Storage.BasePaths) == 0 {
		cfg.Storage.BasePaths = splitList(v.GetString("base_paths"))
	}
	if len(cfg.Storage.BasePaths) == 0 {
		return cfg, fmt.Errorf("storage.base_paths is required")
	}
	if strings.TrimSpace(cfg.Auth.BearerToken) == "" {
		return cfg, fmt.Errorf("auth.bearer_token is required")
	}

	cfg.Detection.PollInterval = v.GetDuration("detection.poll_interval")
	cfg.Server.WriteTimeout = v.GetDuration("server.write_timeout")
	cfg.Detection.Backend = strings.ToLower(strings.TrimSpace(cfg.Detection.Backend))
	if cfg.Detection.Backend == "" {
		cfg.Detection.Backend = "auto"
	}
	switch cfg.Detection.Backend {
	case "auto", "poll", "btrfs":
	default:
		return cfg, fmt.Errorf("detection.backend must be one of: auto, poll, btrfs")
	}
	cfg.Upload.Expiry = v.GetDuration("upload.expiry")
	cfg.Upload.CleanupInterval = v.GetDuration("upload.cleanup_interval")
	if cfg.Detection.PollInterval <= 0 {
		cfg.Detection.PollInterval = 3 * time.Second
	}
	if cfg.Server.WriteTimeout <= 0 {
		cfg.Server.WriteTimeout = 5 * time.Minute
	}
	if cfg.Upload.Expiry <= 0 {
		cfg.Upload.Expiry = 24 * time.Hour
	}
	if cfg.Upload.CleanupInterval <= 0 {
		cfg.Upload.CleanupInterval = 6 * time.Hour
	}
	if cfg.Cache.PathCacheSize <= 0 {
		cfg.Cache.PathCacheSize = 100000
	}
	if cfg.Jobs.Workers <= 0 {
		cfg.Jobs.Workers = defaultJobWorkers()
	}
	if cfg.Jobs.QueueSize <= 0 {
		cfg.Jobs.QueueSize = 8192
	}
	if cfg.Upload.MaxChunkBytes <= 0 {
		cfg.Upload.MaxChunkBytes = int64(50 * 1024 * 1024)
	}
	if cfg.Upload.MaxUploadBytes <= 0 {
		cfg.Upload.MaxUploadBytes = int64(500 * 1024 * 1024)
	}
	if cfg.Upload.MaxChunkedUploadBytes <= 0 {
		cfg.Upload.MaxChunkedUploadBytes = int64(50 * 1024 * 1024 * 1024)
	}
	if cfg.Upload.MaxConcurrentChunkWrites <= 0 {
		cfg.Upload.MaxConcurrentChunkWrites = defaultChunkWriteConcurrency()
	}
	if cfg.Upload.MinFreeBytes < 0 {
		cfg.Upload.MinFreeBytes = 0
	}
	if cfg.Thumbnail.LRUCacheSize <= 0 {
		cfg.Thumbnail.LRUCacheSize = 1024
	}
	if cfg.Thumbnail.MaxSourceBytes <= 0 {
		cfg.Thumbnail.MaxSourceBytes = int64(64 * 1024 * 1024)
	}
	if cfg.Thumbnail.MaxPixels <= 0 {
		cfg.Thumbnail.MaxPixels = int64(40 * 1024 * 1024)
	}

	cfg.Versioning.Cooldown = v.GetDuration("versioning.cooldown")
	cfg.Versioning.PrunerInterval = v.GetDuration("versioning.pruner_interval")
	cfg.Versioning.PinnedGraceAfterDelete = v.GetDuration("versioning.pinned_grace_after_delete")
	cfg.Versioning.Enabled = strings.ToLower(strings.TrimSpace(cfg.Versioning.Enabled))
	switch cfg.Versioning.Enabled {
	case "", "auto":
		cfg.Versioning.Enabled = "auto"
	case "on", "off":
	default:
		return cfg, fmt.Errorf("versioning.enabled must be one of: auto, on, off")
	}
	if cfg.Versioning.Cooldown <= 0 {
		cfg.Versioning.Cooldown = 15 * time.Minute
	}
	if cfg.Versioning.PrunerInterval <= 0 {
		cfg.Versioning.PrunerInterval = 5 * time.Minute
	}
	if cfg.Versioning.PinnedGraceAfterDelete <= 0 {
		cfg.Versioning.PinnedGraceAfterDelete = 30 * 24 * time.Hour
	}
	if cfg.Versioning.MaxLabelBytes <= 0 {
		cfg.Versioning.MaxLabelBytes = 2048
	}
	// max_pinned_per_file: negative is rejected outright because 0 is the
	// "no cap" sentinel. Silently normalising negative -> 0 would let a
	// typo remove the safety limit a careful operator chose.
	if cfg.Versioning.MaxPinnedPerFile < 0 {
		return cfg, fmt.Errorf("versioning.max_pinned_per_file must be >= 0 (use 0 explicitly to disable the cap)")
	}
	if cfg.Versioning.MinSizeForAutoV1 < 0 {
		cfg.Versioning.MinSizeForAutoV1 = 0
	}

	return cfg, nil
}

func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func defaultConfigCandidates() []string {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return []string{
		"/etc/filegate/conf.yaml",
		"/etc/filegate/conf.yml",
		filepath.Join(wd, "conf.yaml"),
		filepath.Join(wd, "conf.yml"),
		"/etc/filegate/config.yaml",
		"/etc/filegate/config.yml",
		filepath.Join(wd, "config.yaml"),
		filepath.Join(wd, "config.yml"),
	}
}

func defaultJobWorkers() int {
	n := runtime.NumCPU() * 4
	if n < 16 {
		return 16
	}
	if n > 256 {
		return 256
	}
	return n
}

func defaultChunkWriteConcurrency() int {
	n := runtime.NumCPU() * 8
	if n < 32 {
		return 32
	}
	if n > 512 {
		return 512
	}
	return n
}
