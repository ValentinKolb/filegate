package cli

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/valentinkolb/filegate/domain"
)

type configFlagKind int

const (
	configFlagString configFlagKind = iota
	configFlagBool
	configFlagInt
	configFlagInt64
	configFlagDuration
	configFlagStringArray
	configFlagS3Keys
	configFlagRetentionBuckets
)

type configFlagSpec struct {
	Name  string
	Path  string
	Kind  configFlagKind
	Usage string
}

func allConfigFlagSpecs() []configFlagSpec {
	return []configFlagSpec{
		{Name: "server-listen", Path: "server.listen", Kind: configFlagString, Usage: "REST listener address"},
		{Name: "server-public-url", Path: "server.public_url", Kind: configFlagString, Usage: "public REST base URL used when minting direct upload URLs"},
		{Name: "server-trusted-proxies", Path: "server.trusted_proxies", Kind: configFlagStringArray, Usage: "proxy IP or CIDR whose X-Forwarded-For is honored; repeat for multiple; empty ignores forward headers"},
		{Name: "server-cors-allowed-origins", Path: "server.cors.allowed_origins", Kind: configFlagStringArray, Usage: "CORS allowed origin; repeat for multiple origins; empty disables CORS"},
		{Name: "server-cors-allowed-methods", Path: "server.cors.allowed_methods", Kind: configFlagStringArray, Usage: "CORS allowed method; repeat for multiple methods; empty uses REST defaults"},
		{Name: "server-cors-allowed-headers", Path: "server.cors.allowed_headers", Kind: configFlagStringArray, Usage: "CORS allowed request header; repeat for multiple headers; empty uses REST defaults"},
		{Name: "server-cors-exposed-headers", Path: "server.cors.exposed_headers", Kind: configFlagStringArray, Usage: "CORS response header exposed to browsers; repeat for multiple headers"},
		{Name: "server-cors-max-age", Path: "server.cors.max_age", Kind: configFlagDuration, Usage: "CORS preflight cache duration"},
		{Name: "server-cors-allow-credentials", Path: "server.cors.allow_credentials", Kind: configFlagBool, Usage: "allow credentials on CORS responses; cannot be used with wildcard origin"},
		{Name: "server-write-timeout", Path: "server.write_timeout", Kind: configFlagDuration, Usage: "HTTP response write timeout"},
		{Name: "server-access-log-enabled", Path: "server.access_log_enabled", Kind: configFlagBool, Usage: "enable REST and S3 access logs"},
		{Name: "server-shutdown-timeout", Path: "server.shutdown_timeout", Kind: configFlagDuration, Usage: "graceful shutdown timeout"},
		{Name: "auth-bearer-token", Path: "auth.bearer_token", Kind: configFlagString, Usage: "REST bearer token"},
		{Name: "storage-base-paths", Path: "storage.base_paths", Kind: configFlagStringArray, Usage: "storage mount path; repeat for multiple mounts"},
		{Name: "storage-index-path", Path: "storage.index_path", Kind: configFlagString, Usage: "Pebble index directory"},
		{Name: "detection-backend", Path: "detection.backend", Kind: configFlagString, Usage: "change detector backend: auto, poll, btrfs"},
		{Name: "detection-poll-interval", Path: "detection.poll_interval", Kind: configFlagDuration, Usage: "polling interval when poll detection is used"},
		{Name: "cache-path-cache-size", Path: "cache.path_cache_size", Kind: configFlagInt, Usage: "in-memory path cache size"},
		{Name: "jobs-workers", Path: "jobs.workers", Kind: configFlagInt, Usage: "background worker count"},
		{Name: "jobs-queue-size", Path: "jobs.queue_size", Kind: configFlagInt, Usage: "background job queue size"},
		{Name: "jobs-thumbnail-workers", Path: "jobs.thumbnail_workers", Kind: configFlagInt, Usage: "thumbnail worker count"},
		{Name: "jobs-thumbnail-queue-size", Path: "jobs.thumbnail_queue_size", Kind: configFlagInt, Usage: "thumbnail job queue size"},
		{Name: "upload-expiry", Path: "upload.expiry", Kind: configFlagDuration, Usage: "chunked upload expiry"},
		{Name: "upload-cleanup-interval", Path: "upload.cleanup_interval", Kind: configFlagDuration, Usage: "chunked upload cleanup interval"},
		{Name: "upload-max-chunk-bytes", Path: "upload.max_chunk_bytes", Kind: configFlagInt64, Usage: "maximum single chunk size in bytes"},
		{Name: "upload-max-upload-bytes", Path: "upload.max_upload_bytes", Kind: configFlagInt64, Usage: "maximum non-chunked upload size in bytes"},
		{Name: "upload-max-chunked-upload-bytes", Path: "upload.max_chunked_upload_bytes", Kind: configFlagInt64, Usage: "maximum chunked upload size in bytes"},
		{Name: "upload-max-concurrent-chunk-writes", Path: "upload.max_concurrent_chunk_writes", Kind: configFlagInt, Usage: "maximum concurrent chunk writes"},
		{Name: "upload-min-free-bytes", Path: "upload.min_free_bytes", Kind: configFlagInt64, Usage: "minimum free bytes required before accepting uploads"},
		{Name: "thumbnail-lru-cache-size", Path: "thumbnail.lru_cache_size", Kind: configFlagInt, Usage: "thumbnail LRU cache size"},
		{Name: "thumbnail-max-source-bytes", Path: "thumbnail.max_source_bytes", Kind: configFlagInt64, Usage: "maximum source file size for thumbnails"},
		{Name: "thumbnail-max-pixels", Path: "thumbnail.max_pixels", Kind: configFlagInt64, Usage: "maximum decoded pixels for thumbnails"},
		{Name: "versioning-enabled", Path: "versioning.enabled", Kind: configFlagString, Usage: "versioning mode: auto, on, off"},
		{Name: "versioning-cooldown", Path: "versioning.cooldown", Kind: configFlagDuration, Usage: "automatic version capture cooldown"},
		{Name: "versioning-min-size-for-auto-v1", Path: "versioning.min_size_for_auto_v1", Kind: configFlagInt64, Usage: "minimum size for automatic V1 capture"},
		{Name: "versioning-retention-bucket", Path: "versioning.retention_buckets", Kind: configFlagRetentionBuckets, Usage: "retention bucket keep_for=<duration>,max_count=<n>; repeat for multiple buckets"},
		{Name: "versioning-pruner-interval", Path: "versioning.pruner_interval", Kind: configFlagDuration, Usage: "versioning pruner interval"},
		{Name: "versioning-max-pinned-per-file", Path: "versioning.max_pinned_per_file", Kind: configFlagInt, Usage: "maximum pinned versions per file; 0 disables cap"},
		{Name: "versioning-pinned-grace-after-delete", Path: "versioning.pinned_grace_after_delete", Kind: configFlagDuration, Usage: "retention grace for pinned versions after live file delete"},
		{Name: "versioning-max-label-bytes", Path: "versioning.max_label_bytes", Kind: configFlagInt, Usage: "maximum version label bytes"},
		{Name: "s3-enabled", Path: "s3.enabled", Kind: configFlagBool, Usage: "enable S3-compatible listener"},
		{Name: "s3-listen", Path: "s3.listen", Kind: configFlagString, Usage: "S3 listener address"},
		{Name: "s3-region", Path: "s3.region", Kind: configFlagString, Usage: "S3 SigV4 region"},
		{Name: "s3-access-key", Path: "s3.access_key", Kind: configFlagString, Usage: "legacy single-tenant S3 access key"},
		{Name: "s3-secret-key", Path: "s3.secret_key", Kind: configFlagString, Usage: "legacy single-tenant S3 secret key"},
		{Name: "s3-max-concurrent-writes", Path: "s3.max_concurrent_writes", Kind: configFlagInt, Usage: "maximum concurrent S3 object and part writes"},
		{Name: "s3-key", Path: "s3.keys", Kind: configFlagS3Keys, Usage: "S3 key access_key=<ak>,secret_key=<sk>,buckets=<a|b|*>,requests_per_second=<n>,burst=<n>; repeat for multiple keys"},
		{Name: "s3-cleanup-done-retention", Path: "s3.cleanup.done_retention", Kind: configFlagDuration, Usage: "multipart done-manifest retention; zero uses adapter default"},
		{Name: "s3-cleanup-aborted-retention", Path: "s3.cleanup.aborted_retention", Kind: configFlagDuration, Usage: "multipart aborted-manifest retention; zero uses adapter default"},
		{Name: "s3-cleanup-stuck-upload-max-age", Path: "s3.cleanup.stuck_upload_max_age", Kind: configFlagDuration, Usage: "maximum age for stuck open multipart uploads; zero uses adapter default"},
		{Name: "s3-cleanup-interval", Path: "s3.cleanup.interval", Kind: configFlagDuration, Usage: "multipart cleanup interval; negative disables"},
		{Name: "metrics-enabled", Path: "metrics.enabled", Kind: configFlagBool, Usage: "enable Prometheus metrics endpoint"},
		{Name: "metrics-path", Path: "metrics.path", Kind: configFlagString, Usage: "Prometheus metrics path"},
		{Name: "metrics-token", Path: "metrics.token", Kind: configFlagString, Usage: "optional Prometheus metrics bearer token"},
	}
}

func registerConfigFlags(flags *pflag.FlagSet) {
	for _, spec := range allConfigFlagSpecs() {
		switch spec.Kind {
		case configFlagString:
			flags.String(spec.Name, "", spec.Usage)
		case configFlagBool:
			flags.Bool(spec.Name, false, spec.Usage)
		case configFlagInt:
			flags.Int(spec.Name, 0, spec.Usage)
		case configFlagInt64:
			flags.Int64(spec.Name, 0, spec.Usage)
		case configFlagDuration:
			flags.Duration(spec.Name, 0, spec.Usage)
		case configFlagStringArray, configFlagS3Keys, configFlagRetentionBuckets:
			flags.StringArray(spec.Name, nil, spec.Usage)
		}
	}
}

func applyChangedConfigFlags(flags *pflag.FlagSet, cfg *domain.Config) error {
	for _, spec := range allConfigFlagSpecs() {
		if !flags.Changed(spec.Name) {
			continue
		}
		if err := applyChangedConfigFlag(flags, spec, cfg); err != nil {
			return err
		}
	}
	return validateResolvedConfig(*cfg)
}

func changedConfigFlagValues(flags *pflag.FlagSet) ([]configYAMLSet, error) {
	var sets []configYAMLSet
	for _, spec := range allConfigFlagSpecs() {
		if !flags.Changed(spec.Name) {
			continue
		}
		value, err := changedConfigFlagValue(flags, spec)
		if err != nil {
			return nil, err
		}
		sets = append(sets, configYAMLSet{Path: spec.Path, Value: value})
	}
	return sets, nil
}

func applyChangedConfigFlag(flags *pflag.FlagSet, spec configFlagSpec, cfg *domain.Config) error {
	switch spec.Path {
	case "server.listen":
		cfg.Server.Listen = getFlagString(flags, spec.Name)
	case "server.public_url":
		cfg.Server.PublicURL = strings.TrimRight(getFlagString(flags, spec.Name), "/")
	case "server.trusted_proxies":
		cfg.Server.TrustedProxies = cleanStringList(getFlagStringArray(flags, spec.Name))
	case "server.cors.allowed_origins":
		cfg.Server.CORS.AllowedOrigins = cleanStringList(getFlagStringArray(flags, spec.Name))
	case "server.cors.allowed_methods":
		cfg.Server.CORS.AllowedMethods = cleanStringList(getFlagStringArray(flags, spec.Name))
	case "server.cors.allowed_headers":
		cfg.Server.CORS.AllowedHeaders = cleanStringList(getFlagStringArray(flags, spec.Name))
	case "server.cors.exposed_headers":
		cfg.Server.CORS.ExposedHeaders = cleanStringList(getFlagStringArray(flags, spec.Name))
	case "server.cors.max_age":
		cfg.Server.CORS.MaxAge = getFlagDuration(flags, spec.Name)
	case "server.cors.allow_credentials":
		cfg.Server.CORS.AllowCredentials = getFlagBool(flags, spec.Name)
	case "server.write_timeout":
		cfg.Server.WriteTimeout = getFlagDuration(flags, spec.Name)
	case "server.access_log_enabled":
		cfg.Server.AccessLogEnabled = getFlagBool(flags, spec.Name)
	case "server.shutdown_timeout":
		cfg.Server.ShutdownTimeout = getFlagDuration(flags, spec.Name)
	case "auth.bearer_token":
		cfg.Auth.BearerToken = getFlagString(flags, spec.Name)
	case "storage.base_paths":
		cfg.Storage.BasePaths = getFlagStringArray(flags, spec.Name)
	case "storage.index_path":
		cfg.Storage.IndexPath = getFlagString(flags, spec.Name)
	case "detection.backend":
		cfg.Detection.Backend = getFlagString(flags, spec.Name)
	case "detection.poll_interval":
		cfg.Detection.PollInterval = getFlagDuration(flags, spec.Name)
	case "cache.path_cache_size":
		cfg.Cache.PathCacheSize = getFlagInt(flags, spec.Name)
	case "jobs.workers":
		cfg.Jobs.Workers = getFlagInt(flags, spec.Name)
	case "jobs.queue_size":
		cfg.Jobs.QueueSize = getFlagInt(flags, spec.Name)
	case "jobs.thumbnail_workers":
		cfg.Jobs.ThumbnailWorkers = getFlagInt(flags, spec.Name)
	case "jobs.thumbnail_queue_size":
		cfg.Jobs.ThumbnailQueueSize = getFlagInt(flags, spec.Name)
	case "upload.expiry":
		cfg.Upload.Expiry = getFlagDuration(flags, spec.Name)
	case "upload.cleanup_interval":
		cfg.Upload.CleanupInterval = getFlagDuration(flags, spec.Name)
	case "upload.max_chunk_bytes":
		cfg.Upload.MaxChunkBytes = getFlagInt64(flags, spec.Name)
	case "upload.max_upload_bytes":
		cfg.Upload.MaxUploadBytes = getFlagInt64(flags, spec.Name)
	case "upload.max_chunked_upload_bytes":
		cfg.Upload.MaxChunkedUploadBytes = getFlagInt64(flags, spec.Name)
	case "upload.max_concurrent_chunk_writes":
		cfg.Upload.MaxConcurrentChunkWrites = getFlagInt(flags, spec.Name)
	case "upload.min_free_bytes":
		cfg.Upload.MinFreeBytes = getFlagInt64(flags, spec.Name)
	case "thumbnail.lru_cache_size":
		cfg.Thumbnail.LRUCacheSize = getFlagInt(flags, spec.Name)
	case "thumbnail.max_source_bytes":
		cfg.Thumbnail.MaxSourceBytes = getFlagInt64(flags, spec.Name)
	case "thumbnail.max_pixels":
		cfg.Thumbnail.MaxPixels = getFlagInt64(flags, spec.Name)
	case "versioning.enabled":
		cfg.Versioning.Enabled = getFlagString(flags, spec.Name)
	case "versioning.cooldown":
		cfg.Versioning.Cooldown = getFlagDuration(flags, spec.Name)
	case "versioning.min_size_for_auto_v1":
		cfg.Versioning.MinSizeForAutoV1 = getFlagInt64(flags, spec.Name)
	case "versioning.retention_buckets":
		buckets, err := parseRetentionBucketFlags(getFlagStringArray(flags, spec.Name))
		if err != nil {
			return err
		}
		cfg.Versioning.RetentionBuckets = buckets
	case "versioning.pruner_interval":
		cfg.Versioning.PrunerInterval = getFlagDuration(flags, spec.Name)
	case "versioning.max_pinned_per_file":
		cfg.Versioning.MaxPinnedPerFile = getFlagInt(flags, spec.Name)
	case "versioning.pinned_grace_after_delete":
		cfg.Versioning.PinnedGraceAfterDelete = getFlagDuration(flags, spec.Name)
	case "versioning.max_label_bytes":
		cfg.Versioning.MaxLabelBytes = getFlagInt(flags, spec.Name)
	case "s3.enabled":
		cfg.S3.Enabled = getFlagBool(flags, spec.Name)
	case "s3.listen":
		cfg.S3.Listen = getFlagString(flags, spec.Name)
	case "s3.region":
		cfg.S3.Region = getFlagString(flags, spec.Name)
	case "s3.access_key":
		cfg.S3.AccessKey = getFlagString(flags, spec.Name)
	case "s3.secret_key":
		cfg.S3.SecretKey = getFlagString(flags, spec.Name)
	case "s3.max_concurrent_writes":
		cfg.S3.MaxConcurrentWrites = getFlagInt(flags, spec.Name)
	case "s3.keys":
		keys, err := parseS3KeyFlags(getFlagStringArray(flags, spec.Name))
		if err != nil {
			return err
		}
		cfg.S3.Keys = keys
	case "s3.cleanup.done_retention":
		cfg.S3.Cleanup.DoneRetention = getFlagDuration(flags, spec.Name)
	case "s3.cleanup.aborted_retention":
		cfg.S3.Cleanup.AbortedRetention = getFlagDuration(flags, spec.Name)
	case "s3.cleanup.stuck_upload_max_age":
		cfg.S3.Cleanup.StuckUploadMaxAge = getFlagDuration(flags, spec.Name)
	case "s3.cleanup.interval":
		cfg.S3.Cleanup.Interval = getFlagDuration(flags, spec.Name)
	case "metrics.enabled":
		cfg.Metrics.Enabled = getFlagBool(flags, spec.Name)
	case "metrics.path":
		cfg.Metrics.Path = getFlagString(flags, spec.Name)
	case "metrics.token":
		cfg.Metrics.Token = getFlagString(flags, spec.Name)
	default:
		return fmt.Errorf("unhandled config flag path %q", spec.Path)
	}
	return nil
}

func changedConfigFlagValue(flags *pflag.FlagSet, spec configFlagSpec) (any, error) {
	switch spec.Kind {
	case configFlagString:
		return getFlagString(flags, spec.Name), nil
	case configFlagBool:
		return getFlagBool(flags, spec.Name), nil
	case configFlagInt:
		return getFlagInt(flags, spec.Name), nil
	case configFlagInt64:
		return getFlagInt64(flags, spec.Name), nil
	case configFlagDuration:
		return getFlagDuration(flags, spec.Name).String(), nil
	case configFlagStringArray:
		return getFlagStringArray(flags, spec.Name), nil
	case configFlagS3Keys:
		return parseS3KeyFlags(getFlagStringArray(flags, spec.Name))
	case configFlagRetentionBuckets:
		return parseRetentionBucketFlags(getFlagStringArray(flags, spec.Name))
	default:
		return nil, fmt.Errorf("unhandled config flag kind for %s", spec.Name)
	}
}

func getFlagString(flags *pflag.FlagSet, name string) string {
	v, _ := flags.GetString(name)
	return v
}

func getFlagBool(flags *pflag.FlagSet, name string) bool {
	v, _ := flags.GetBool(name)
	return v
}

func getFlagInt(flags *pflag.FlagSet, name string) int {
	v, _ := flags.GetInt(name)
	return v
}

func getFlagInt64(flags *pflag.FlagSet, name string) int64 {
	v, _ := flags.GetInt64(name)
	return v
}

func getFlagDuration(flags *pflag.FlagSet, name string) time.Duration {
	v, _ := flags.GetDuration(name)
	return v
}

func getFlagStringArray(flags *pflag.FlagSet, name string) []string {
	v, _ := flags.GetStringArray(name)
	return v
}

func parseRetentionBucketFlags(raw []string) ([]domain.RetentionBucketConfig, error) {
	out := make([]domain.RetentionBucketConfig, 0, len(raw))
	for i, entry := range raw {
		kv := parseKVParts(entry)
		keepRaw := strings.TrimSpace(kv["keep_for"])
		if keepRaw == "" {
			keepRaw = strings.TrimSpace(kv["keep-for"])
		}
		if keepRaw == "" {
			return nil, fmt.Errorf("versioning-retention-bucket[%d]: keep_for is required", i)
		}
		keepFor, err := time.ParseDuration(keepRaw)
		if err != nil {
			return nil, fmt.Errorf("versioning-retention-bucket[%d]: keep_for: %w", i, err)
		}
		maxRaw := strings.TrimSpace(kv["max_count"])
		if maxRaw == "" {
			maxRaw = strings.TrimSpace(kv["max-count"])
		}
		if maxRaw == "" {
			return nil, fmt.Errorf("versioning-retention-bucket[%d]: max_count is required", i)
		}
		maxCount, err := strconv.Atoi(maxRaw)
		if err != nil {
			return nil, fmt.Errorf("versioning-retention-bucket[%d]: max_count: %w", i, err)
		}
		out = append(out, domain.RetentionBucketConfig{KeepFor: keepFor, MaxCount: maxCount})
	}
	return out, nil
}

func parseS3KeyFlags(raw []string) ([]domain.S3KeyConfig, error) {
	out := make([]domain.S3KeyConfig, 0, len(raw))
	for i, entry := range raw {
		kv := parseKVParts(entry)
		accessKey := firstNonEmpty(kv["access_key"], kv["access-key"], kv["access"])
		secretKey := firstNonEmpty(kv["secret_key"], kv["secret-key"], kv["secret"])
		if accessKey == "" || secretKey == "" {
			return nil, fmt.Errorf("s3-key[%d]: access_key and secret_key are required", i)
		}
		rps, err := parseOptionalNonNegativeInt(firstNonEmpty(kv["requests_per_second"], kv["requests-per-second"], kv["rps"]), "requests_per_second")
		if err != nil {
			return nil, fmt.Errorf("s3-key[%d]: %w", i, err)
		}
		burst, err := parseOptionalNonNegativeInt(kv["burst"], "burst")
		if err != nil {
			return nil, fmt.Errorf("s3-key[%d]: %w", i, err)
		}
		out = append(out, domain.S3KeyConfig{
			AccessKey:         accessKey,
			SecretKey:         secretKey,
			Buckets:           splitPipeList(kv["buckets"]),
			RequestsPerSecond: rps,
			Burst:             burst,
		})
	}
	return out, nil
}

func parseKVParts(raw string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			out[strings.ToLower(strings.TrimSpace(part))] = ""
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func splitPipeList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseOptionalNonNegativeInt(raw, name string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s must be >= 0", name)
	}
	return n, nil
}

func validateResolvedConfig(cfg domain.Config) error {
	if len(cfg.Storage.BasePaths) == 0 {
		return fmt.Errorf("storage.base_paths is required")
	}
	if strings.TrimSpace(cfg.Auth.BearerToken) == "" && !cfg.S3.Enabled {
		return fmt.Errorf("auth.bearer_token is required (unless s3.enabled=true for an S3-only deployment)")
	}
	if err := validatePublicURL(cfg.Server.PublicURL); err != nil {
		return err
	}
	if err := validateCORSConfig(cfg.Server.CORS); err != nil {
		return err
	}
	backend := strings.ToLower(strings.TrimSpace(cfg.Detection.Backend))
	if backend == "" {
		backend = "auto"
	}
	switch backend {
	case "auto", "poll", "btrfs":
	default:
		return fmt.Errorf("detection.backend must be one of: auto, poll, btrfs")
	}
	if cfg.Upload.MaxChunkedUploadBytes < cfg.Upload.MaxChunkBytes {
		return fmt.Errorf("upload.max_chunked_upload_bytes must be >= upload.max_chunk_bytes")
	}
	versioningEnabled := strings.ToLower(strings.TrimSpace(cfg.Versioning.Enabled))
	switch versioningEnabled {
	case "", "auto", "on", "off":
	default:
		return fmt.Errorf("versioning.enabled must be one of: auto, on, off")
	}
	if cfg.Versioning.MaxPinnedPerFile < 0 {
		return fmt.Errorf("versioning.max_pinned_per_file must be >= 0 (use 0 explicitly to disable the cap)")
	}
	if err := validateS3Config(cfg); err != nil {
		return err
	}
	return nil
}

func validatePublicURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("server.public_url must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("server.public_url must use http or https")
	}
	return nil
}

func validateCORSConfig(cfg domain.CORSConfig) error {
	origins := cleanStringList(cfg.AllowedOrigins)
	if len(origins) == 0 {
		return nil
	}
	if cfg.MaxAge < 0 {
		return fmt.Errorf("server.cors.max_age must be >= 0")
	}
	for _, origin := range origins {
		if origin == "*" {
			if cfg.AllowCredentials {
				return fmt.Errorf("server.cors.allow_credentials cannot be true when allowed_origins contains *")
			}
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("server.cors.allowed_origins must contain origins like https://app.example.com")
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("server.cors.allowed_origins must use http or https")
		}
	}
	return nil
}

func cleanStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func validateS3Config(cfg domain.Config) error {
	mounts := configuredMountNames(cfg.Storage.BasePaths)
	if cfg.S3.MaxConcurrentWrites < 0 {
		return fmt.Errorf("s3.max_concurrent_writes must be >= 0 (use 0 explicitly for the default)")
	}
	if cfg.S3.Enabled {
		if cfg.S3.AccessKey == "" && cfg.S3.SecretKey == "" && len(cfg.S3.Keys) == 0 {
			return fmt.Errorf("s3.enabled=true requires either s3.access_key/s3.secret_key or at least one s3.keys entry")
		}
		for name := range mounts {
			if err := domain.ValidateBucketName(name); err != nil {
				return err
			}
		}
	}
	seen := map[string]struct{}{}
	if cfg.S3.AccessKey != "" || cfg.S3.SecretKey != "" {
		if cfg.S3.AccessKey == "" || cfg.S3.SecretKey == "" {
			return fmt.Errorf("s3.access_key and s3.secret_key must be set together")
		}
		seen[cfg.S3.AccessKey] = struct{}{}
	}
	for i, key := range cfg.S3.Keys {
		if key.AccessKey == "" || key.SecretKey == "" {
			return fmt.Errorf("s3.keys[%d]: access_key and secret_key must be non-empty", i)
		}
		if _, ok := seen[key.AccessKey]; ok {
			return fmt.Errorf("s3.keys[%d]: access key %q is duplicated", i, key.AccessKey)
		}
		seen[key.AccessKey] = struct{}{}
		if key.RequestsPerSecond < 0 {
			return fmt.Errorf("s3.keys[%d]: requests_per_second must be >= 0", i)
		}
		if key.Burst < 0 {
			return fmt.Errorf("s3.keys[%d]: burst must be >= 0", i)
		}
		for _, bucket := range key.Buckets {
			bucket = strings.TrimSpace(bucket)
			if bucket == "" || bucket == "*" {
				continue
			}
			if _, ok := mounts[bucket]; !ok {
				return fmt.Errorf("s3.keys[%d]: bucket %q is not a configured mount", i, bucket)
			}
		}
	}
	return nil
}

func configuredMountNames(paths []string) map[string]struct{} {
	out := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		name := filepath.Base(filepath.Clean(p))
		if name != "." && name != string(filepath.Separator) && strings.TrimSpace(name) != "" {
			out[name] = struct{}{}
		}
	}
	return out
}
