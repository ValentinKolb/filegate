package domain

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileLockMapSerializesSameFile(t *testing.T) {
	locks := newFileLockMap()
	fileID := FileID{0x01}

	var inFlight atomic.Int32
	var maxObserved atomic.Int32
	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			_ = locks.With(fileID, func() error {
				cur := inFlight.Add(1)
				for {
					prev := maxObserved.Load()
					if cur <= prev || maxObserved.CompareAndSwap(prev, cur) {
						break
					}
				}
				time.Sleep(2 * time.Millisecond)
				inFlight.Add(-1)
				return nil
			})
		}()
	}
	wg.Wait()

	if got := maxObserved.Load(); got != 1 {
		t.Fatalf("max concurrent in critical section = %d, want 1", got)
	}
}

func TestFileLockMapAllowsParallelDifferentFiles(t *testing.T) {
	locks := newFileLockMap()
	const files = 8

	gate := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(files)
	var inFlight atomic.Int32
	var maxObserved atomic.Int32
	var wg sync.WaitGroup
	wg.Add(files)

	for i := 0; i < files; i++ {
		fid := FileID{}
		fid[0] = byte(i)
		go func() {
			defer wg.Done()
			ready.Done()
			<-gate
			_ = locks.With(fid, func() error {
				cur := inFlight.Add(1)
				for {
					prev := maxObserved.Load()
					if cur <= prev || maxObserved.CompareAndSwap(prev, cur) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				inFlight.Add(-1)
				return nil
			})
		}()
	}
	ready.Wait()
	close(gate)
	wg.Wait()

	if got := maxObserved.Load(); got < 2 {
		t.Fatalf("expected ≥ 2 concurrent for distinct files, observed %d", got)
	}
}

func TestFileLockMapAcquireReturnsSameMutex(t *testing.T) {
	locks := newFileLockMap()
	fileID := FileID{0x42}
	a := locks.Acquire(fileID)
	b := locks.Acquire(fileID)
	if a != b {
		t.Fatalf("Acquire returned different mutexes for the same fileID")
	}
}

func TestFileLockMapWithPropagatesError(t *testing.T) {
	locks := newFileLockMap()
	wantErr := ErrInvalidArgument
	got := locks.With(FileID{0x07}, func() error { return wantErr })
	if got != wantErr {
		t.Fatalf("err=%v, want %v", got, wantErr)
	}
}
