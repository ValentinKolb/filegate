package pebble

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/valentinkolb/filegate/domain"
)

func TestIndexCloseRaceNoPanic(t *testing.T) {
	idx, err := Open(t.TempDir(), 8<<20)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()

	id := domain.FileID(uuid.MustParse("019cb9ae-76c1-7807-ba50-cbb05a08ec6c"))
	putErr := idx.Batch(func(b domain.Batch) error {
		b.PutEntity(domain.Entity{
			ID:       id,
			ParentID: domain.FileID{},
			Name:     "x",
			IsDir:    false,
			Size:     1,
			Mtime:    time.Now().UnixMilli(),
			UID:      1000,
			GID:      1000,
			Mode:     0o644,
		})
		return nil
	})
	if putErr != nil {
		t.Fatalf("seed entity: %v", putErr)
	}

	const workers = 16
	const minOpsPerWorker = 50

	stop := make(chan struct{})
	var wg sync.WaitGroup
	panicCh := make(chan interface{}, 1)
	ops := make([]atomic.Int64, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					select {
					case panicCh <- rec:
					default:
					}
				}
			}()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = idx.GetEntity(id)
				ops[slot].Add(1)
			}
		}(w)
	}

	// Wait until every worker has actually run several iterations. This makes
	// sure Close races real in-flight reads instead of just a few that
	// happened to start instantly.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ready := true
		for i := range ops {
			if ops[i].Load() < minOpsPerWorker {
				ready = false
				break
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("workers did not warm up in time")
		}
	}

	_ = idx.Close()
	close(stop)
	wg.Wait()

	select {
	case rec := <-panicCh:
		t.Fatalf("unexpected panic during close race: %v", rec)
	default:
	}

	_, err = idx.GetEntity(id)
	if !errors.Is(err, ErrIndexClosed) && !errors.Is(err, ErrIndexUnavailable) {
		t.Fatalf("get after close err=%v, want index closed/unavailable", err)
	}
}

func TestIndexReturnsClosedForBatchAfterClose(t *testing.T) {
	idx, err := Open(t.TempDir(), 8<<20)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err = idx.Batch(func(b domain.Batch) error { return nil })
	if !errors.Is(err, ErrIndexClosed) {
		t.Fatalf("batch err=%v, want ErrIndexClosed", err)
	}
}
