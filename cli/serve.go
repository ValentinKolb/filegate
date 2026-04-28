package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	httpadapter "github.com/valentinkolb/filegate/adapter/http"
	"github.com/valentinkolb/filegate/domain"
	"github.com/valentinkolb/filegate/infra/detect"
	"github.com/valentinkolb/filegate/infra/eventbus"
	"github.com/valentinkolb/filegate/infra/filesystem"
	indexpebble "github.com/valentinkolb/filegate/infra/pebble"

	"github.com/spf13/cobra"
)

func newDaemonServeCmd() *cobra.Command {
	var configFile string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start Filegate HTTP server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOOS != "linux" {
				return fmt.Errorf("filegate v2 currently supports linux only")
			}

			cfg, err := loadConfig(configFile)
			if err != nil {
				return err
			}

			idx, svc, err := buildCore(cfg)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			detector, err := detect.New(cfg.Detection.Backend, cfg.Storage.BasePaths, cfg.Detection.PollInterval)
			if err != nil {
				_ = idx.Close()
				return err
			}
			log.Printf("[filegate] detection backend: %s", detector.Name())
			detector.Start(ctx)
			detectorDone := make(chan struct{})
			go func() {
				defer close(detectorDone)
				consumeDetectorEvents(ctx, svc, detector.Events())
			}()

			router := httpadapter.NewRouter(svc, httpadapter.RouterOptions{
				BearerToken:              cfg.Auth.BearerToken,
				AccessLogEnabled:         cfg.Server.AccessLogEnabled,
				IndexPath:                cfg.Storage.IndexPath,
				JobWorkers:               cfg.Jobs.Workers,
				JobQueueSize:             cfg.Jobs.QueueSize,
				ThumbnailJobWorkers:      cfg.Jobs.ThumbnailWorkers,
				ThumbnailJobQueueSize:    cfg.Jobs.ThumbnailQueueSize,
				UploadExpiry:             cfg.Upload.Expiry,
				UploadCleanupInterval:    cfg.Upload.CleanupInterval,
				MaxChunkBytes:            cfg.Upload.MaxChunkBytes,
				MaxUploadBytes:           cfg.Upload.MaxUploadBytes,
				MaxChunkedUploadBytes:    cfg.Upload.MaxChunkedUploadBytes,
				MaxConcurrentChunkWrites: cfg.Upload.MaxConcurrentChunkWrites,
				UploadMinFreeBytes:       cfg.Upload.MinFreeBytes,
				ThumbnailLRUCacheSize:    cfg.Thumbnail.LRUCacheSize,
				ThumbnailMaxSourceBytes:  cfg.Thumbnail.MaxSourceBytes,
				ThumbnailMaxPixels:       cfg.Thumbnail.MaxPixels,
				Rescan:                   svc.Rescan,
			})
			var routerCloser interface{ Close() error }
			if closer, ok := router.(interface{ Close() error }); ok {
				routerCloser = closer
			}

			srv := &http.Server{
				Addr:              cfg.Server.Listen,
				Handler:           router,
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      cfg.Server.WriteTimeout,
				IdleTimeout:       120 * time.Second,
				MaxHeaderBytes:    1 << 20,
			}
			errCh := make(chan error, 1)
			go func() {
				log.Printf("[filegate] listening on %s", cfg.Server.Listen)
				errCh <- srv.ListenAndServe()
			}()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			select {
			case sig := <-sigCh:
				log.Printf("[filegate] received signal %s, shutting down", sig)
			case err := <-errCh:
				if err != nil && !errors.Is(err, http.ErrServerClosed) {
					cancel()
					detector.Close()
					<-detectorDone
					if routerCloser != nil {
						_ = routerCloser.Close()
					}
					_ = idx.Close()
					return err
				}
			}

			shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_ = srv.Shutdown(shutdownCtx)
			cancel()
			detector.Close()
			<-detectorDone
			if routerCloser != nil {
				_ = routerCloser.Close()
			}
			_ = idx.Close()
			return nil
		},
	}
	cmd.Flags().StringVar(&configFile, "config", "", "path to config file")
	return cmd
}

func buildCore(cfg domain.Config) (*indexpebble.Index, *domain.Service, error) {
	if err := os.MkdirAll(cfg.Storage.IndexPath, 0o755); err != nil {
		return nil, nil, err
	}
	idx, err := indexpebble.Open(cfg.Storage.IndexPath, 128<<20)
	if err != nil && errors.Is(err, indexpebble.ErrUnsupportedIndexFormat) {
		log.Printf("[filegate] index format mismatch, rebuilding index at %s", cfg.Storage.IndexPath)
		if rmErr := os.RemoveAll(cfg.Storage.IndexPath); rmErr != nil {
			return nil, nil, rmErr
		}
		if mkErr := os.MkdirAll(cfg.Storage.IndexPath, 0o755); mkErr != nil {
			return nil, nil, mkErr
		}
		idx, err = indexpebble.Open(cfg.Storage.IndexPath, 128<<20)
	}
	if err != nil {
		return nil, nil, err
	}
	svc, err := domain.NewService(idx, filesystem.New(), eventbus.New(), cfg.Storage.BasePaths, cfg.Cache.PathCacheSize)
	if err != nil {
		_ = idx.Close()
		return nil, nil, err
	}
	return idx, svc, nil
}

func consumeDetectorEvents(ctx context.Context, svc *domain.Service, events <-chan []detect.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case batch, ok := <-events:
			if !ok {
				return
			}
			if len(batch) == 0 {
				continue
			}
			merged := coalesceDetectorBatches(ctx, events, batch)
			if len(merged) == 0 {
				continue
			}
			if err := applyDetectorBatch(svc, merged); err != nil {
				if isDetectorTerminalError(err) {
					log.Printf("[filegate] detector stopping: %v", err)
					return
				}
				log.Printf("[filegate] detector batch apply failed: %v, falling back to full rescan", err)
				if rescanErr := svc.Rescan(); rescanErr != nil {
					if isDetectorTerminalError(rescanErr) {
						log.Printf("[filegate] detector stopping after rescan error: %v", rescanErr)
						return
					}
					log.Printf("[filegate] fallback rescan failed: %v", rescanErr)
				}
			}
		}
	}
}

func coalesceDetectorBatches(ctx context.Context, events <-chan []detect.Event, first []detect.Event) []detect.Event {
	combined := append([]detect.Event(nil), first...)
	for {
		select {
		case <-ctx.Done():
			return combined
		case next, ok := <-events:
			if !ok {
				return combined
			}
			if len(next) == 0 {
				continue
			}
			combined = append(combined, next...)
		default:
			return combined
		}
	}
}

func applyDetectorBatch(svc *domain.Service, batch []detect.Event) error {
	if len(batch) == 0 {
		return nil
	}

	priority := map[detect.EventType]int{
		detect.EventUnknown: 4,
		detect.EventDeleted: 3,
		detect.EventChanged: 2,
		detect.EventCreated: 1,
	}
	byPath := make(map[string]detect.Event, len(batch))
	for _, ev := range batch {
		abs := strings.TrimSpace(ev.AbsPath)
		if abs == "" {
			continue
		}
		if cur, ok := byPath[abs]; ok {
			if priority[ev.Type] >= priority[cur.Type] {
				byPath[abs] = ev
			}
			continue
		}
		byPath[abs] = ev
	}
	if len(byPath) == 0 {
		return nil
	}

	unknownPaths := make([]string, 0, 8)
	unknownBases := make([]string, 0, 4)
	deletePaths := make([]string, 0, len(byPath))
	syncPaths := make([]string, 0, len(byPath))
	for abs, ev := range byPath {
		switch ev.Type {
		case detect.EventUnknown:
			unknownPaths = append(unknownPaths, abs)
			if strings.TrimSpace(ev.Base) != "" {
				unknownBases = append(unknownBases, ev.Base)
			}
		case detect.EventDeleted:
			deletePaths = append(deletePaths, abs)
		case detect.EventCreated, detect.EventChanged:
			syncPaths = append(syncPaths, abs)
		}
	}

	if len(unknownPaths) > 0 {
		if len(unknownBases) == 0 {
			return svc.Rescan()
		}
		for _, base := range uniqueStrings(unknownBases) {
			if err := svc.RescanMount(base); err != nil {
				return err
			}
		}
		return nil
	}

	sort.Slice(deletePaths, func(i, j int) bool { return len(deletePaths[i]) > len(deletePaths[j]) })
	for _, abs := range deletePaths {
		if err := svc.RemoveAbsPath(abs); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
	}

	sort.Strings(syncPaths)
	for _, abs := range syncPaths {
		if err := svc.SyncAbsPath(abs); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				if rmErr := svc.RemoveAbsPath(abs); rmErr != nil && !errors.Is(rmErr, domain.ErrNotFound) {
					return rmErr
				}
				continue
			}
			return err
		}
	}

	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func isDetectorTerminalError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, indexpebble.ErrIndexClosed) || errors.Is(err, indexpebble.ErrIndexUnavailable) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "pebble: closed")
}
