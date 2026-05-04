package cli

import (
	"context"
	"log"
	"time"

	"github.com/valentinkolb/filegate/domain"
	"github.com/valentinkolb/filegate/infra/detect"
)

// versioningShouldEnable resolves the operator's versioning.enabled
// setting to a definitive on/off boolean.
//
//   - "off" : feature disabled, regardless of filesystem.
//   - "on"  : feature enabled. The user explicitly opted in; no btrfs
//     check (operator is responsible for capability).
//   - "auto" (default): on iff every base_path is btrfs. We check once
//     at startup; live mount changes are not detected.
func versioningShouldEnable(cfg domain.VersioningConfig, basePaths []string) bool {
	switch cfg.Enabled {
	case "off":
		return false
	case "on":
		return true
	}
	// auto
	ok, err := detect.SupportsBTRFS(context.Background(), basePaths)
	if err != nil {
		log.Printf("[filegate] versioning auto-detect: btrfs check failed: %v — disabling feature", err)
		return false
	}
	return ok
}

// runVersioningPruner periodically calls Service.PruneVersions until the
// context is cancelled. The first prune happens after one interval (not
// at startup) so a flapping daemon doesn't immediately churn through
// reflinked blobs after every restart.
func runVersioningPruner(ctx context.Context, svc *domain.Service, interval time.Duration, done chan<- struct{}) {
	defer close(done)
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats, err := svc.PruneVersions()
			if err != nil {
				log.Printf("[filegate] versioning pruner: %v", err)
				continue
			}
			if stats.VersionsDeleted > 0 || stats.OrphansPurged > 0 {
				log.Printf("[filegate] versioning pruner: scanned=%d kept=%d deleted=%d orphans=%d errors=%d",
					stats.FilesScanned, stats.VersionsKept, stats.VersionsDeleted,
					stats.OrphansPurged, stats.Errors)
			}
		}
	}
}
