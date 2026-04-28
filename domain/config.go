package domain

import "time"

// Config is the top-level configuration for the Filegate service.
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Auth      AuthConfig      `mapstructure:"auth"`
	Storage   StorageConfig   `mapstructure:"storage"`
	Detection DetectionConfig `mapstructure:"detection"`
	Cache     CacheConfig     `mapstructure:"cache"`
	Jobs      JobsConfig      `mapstructure:"jobs"`
	Upload    UploadConfig    `mapstructure:"upload"`
	Thumbnail ThumbnailConfig `mapstructure:"thumbnail"`
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
