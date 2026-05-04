package domain

import "time"

// Config is the top-level configuration for the Filegate service.
type Config struct {
	Server     ServerConfig     `mapstructure:"server"`
	Auth       AuthConfig       `mapstructure:"auth"`
	Storage    StorageConfig    `mapstructure:"storage"`
	Detection  DetectionConfig  `mapstructure:"detection"`
	Cache      CacheConfig      `mapstructure:"cache"`
	Jobs       JobsConfig       `mapstructure:"jobs"`
	Upload     UploadConfig     `mapstructure:"upload"`
	Thumbnail  ThumbnailConfig  `mapstructure:"thumbnail"`
	Versioning VersioningConfig `mapstructure:"versioning"`
}

// ServerConfig controls HTTP server behavior.
type ServerConfig struct {
	Listen           string        `mapstructure:"listen"`
	WriteTimeout     time.Duration `mapstructure:"write_timeout"`
	AccessLogEnabled bool          `mapstructure:"access_log_enabled"`
}

// AuthConfig contains authentication settings.
type AuthConfig struct {
	BearerToken string `mapstructure:"bearer_token"`
}

// StorageConfig specifies filesystem paths for data and index storage.
type StorageConfig struct {
	BasePaths []string `mapstructure:"base_paths"`
	IndexPath string   `mapstructure:"index_path"`
}

// DetectionConfig controls the filesystem change detection backend.
type DetectionConfig struct {
	Backend      string        `mapstructure:"backend"`
	PollInterval time.Duration `mapstructure:"poll_interval"`
}

// CacheConfig controls in-memory cache sizes.
type CacheConfig struct {
	PathCacheSize int `mapstructure:"path_cache_size"`
}

// JobsConfig controls the bounded worker pool for background tasks.
type JobsConfig struct {
	Workers            int `mapstructure:"workers"`
	QueueSize          int `mapstructure:"queue_size"`
	ThumbnailWorkers   int `mapstructure:"thumbnail_workers"`
	ThumbnailQueueSize int `mapstructure:"thumbnail_queue_size"`
}

// UploadConfig controls chunked upload lifecycle and limits.
type UploadConfig struct {
	Expiry                   time.Duration `mapstructure:"expiry"`
	CleanupInterval          time.Duration `mapstructure:"cleanup_interval"`
	MaxChunkBytes            int64         `mapstructure:"max_chunk_bytes"`
	MaxUploadBytes           int64         `mapstructure:"max_upload_bytes"`
	MaxChunkedUploadBytes    int64         `mapstructure:"max_chunked_upload_bytes"`
	MaxConcurrentChunkWrites int           `mapstructure:"max_concurrent_chunk_writes"`
	MinFreeBytes             int64         `mapstructure:"min_free_bytes"`
}

// ThumbnailConfig controls on-demand thumbnail generation behavior.
type ThumbnailConfig struct {
	LRUCacheSize   int   `mapstructure:"lru_cache_size"`
	MaxSourceBytes int64 `mapstructure:"max_source_bytes"`
	MaxPixels      int64 `mapstructure:"max_pixels"`
}

// VersioningConfig controls per-file version history. The feature is
// HTTP-only (external writes via cp/rsync are not captured) and requires
// btrfs reflinks for storage efficiency. Per-mount auto-detection makes
// btrfs mounts opt-in and ext4 mounts no-op.
//
// Enabled values:
//   - "auto"  : btrfs mounts get versioning, ext4 silently disabled
//   - "on"    : forced on; non-btrfs mounts return ErrUnsupportedFS
//   - "off"   : feature globally disabled
//
// Cooldown bounds auto-capture noise: a write within Cooldown of the
// previous version captures nothing. Manual snapshots ignore cooldown.
//
// MinSizeForAutoV1 skips automatic V1 capture for tiny files where
// reflink savings don't offset metadata churn (config files, dotfiles).
// Manual snapshots ignore the floor.
type VersioningConfig struct {
	Enabled                string                  `mapstructure:"enabled"`
	Cooldown               time.Duration           `mapstructure:"cooldown"`
	MinSizeForAutoV1       int64                   `mapstructure:"min_size_for_auto_v1"`
	RetentionBuckets       []RetentionBucketConfig `mapstructure:"retention_buckets"`
	PrunerInterval         time.Duration           `mapstructure:"pruner_interval"`
	MaxPinnedPerFile       int                     `mapstructure:"max_pinned_per_file"`
	PinnedGraceAfterDelete time.Duration           `mapstructure:"pinned_grace_after_delete"`
	MaxLabelBytes          int                     `mapstructure:"max_label_bytes"`
}

// RetentionBucketConfig defines one age window of the bucketed exponential
// decay retention. KeepFor is the bucket window from "now"; MaxCount is the
// number of versions to retain inside it (-1 = unlimited).
//
// The pruner places target points evenly across each bucket and keeps the
// nearest version to each target. Newer buckets win on overlap so a
// version is never double-counted.
type RetentionBucketConfig struct {
	KeepFor  time.Duration `mapstructure:"keep_for"`
	MaxCount int           `mapstructure:"max_count"`
}
