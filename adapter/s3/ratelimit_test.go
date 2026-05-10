package s3

import (
	"sync"
	"testing"
	"time"
)

// TestTokenBucketAcceptsBurstThenRejects: a fresh bucket has
// `capacity` tokens — the first `capacity` consume() calls
// succeed, the next one must reject.
func TestTokenBucketAcceptsBurstThenRejects(t *testing.T) {
	b := newTokenBucket(10, 5) // 10 tokens/sec, burst 5
	for i := 0; i < 5; i++ {
		if !b.consume() {
			t.Fatalf("consume #%d rejected, want accept (burst=5)", i+1)
		}
	}
	if b.consume() {
		t.Fatalf("consume #6 accepted, want reject (bucket should be empty)")
	}
}

// TestTokenBucketRefills: after the bucket empties, the next
// successful consume must wait (slightly more than) 1/rate
// seconds. We verify by measuring elapsed time across a
// drain → wait → consume sequence.
func TestTokenBucketRefills(t *testing.T) {
	b := newTokenBucket(20, 1) // 20 tokens/sec, burst 1
	if !b.consume() {
		t.Fatalf("first consume rejected")
	}
	if b.consume() {
		t.Fatalf("second consume right after drain accepted")
	}
	// 1 token at 20 tokens/sec = 50ms refill.
	time.Sleep(70 * time.Millisecond)
	if !b.consume() {
		t.Fatalf("consume after 70ms refill rejected")
	}
}

// TestTokenBucketCapsAtBurst: after a long idle period, the
// bucket must NOT contain more than `capacity` tokens. Otherwise
// a key could go silent for an hour and then issue a giant
// burst of requests, defeating the rate-limit.
func TestTokenBucketCapsAtBurst(t *testing.T) {
	b := newTokenBucket(100, 3) // 100 tps, burst 3
	// Force a "long idle" by reaching into last and rewinding.
	b.mu.Lock()
	b.last = time.Now().Add(-10 * time.Second)
	b.mu.Unlock()
	// First consume refills lazily — should top up to capacity (3),
	// then take 1 → 2 left. Three consume()s must succeed; fourth fails.
	for i := 0; i < 3; i++ {
		if !b.consume() {
			t.Fatalf("post-idle consume #%d rejected, want accept (cap=3)", i+1)
		}
	}
	if b.consume() {
		t.Fatalf("consume #4 after 10s idle accepted — bucket exceeded capacity, no rate-limit")
	}
}

// TestRateLimiterUnconfiguredKeysUnlimited: a key without
// RequestsPerSecond MUST always be admitted. Pre-fix a
// broken-default would silently throttle every key.
func TestRateLimiterUnconfiguredKeysUnlimited(t *testing.T) {
	rl := newRateLimiter([]KeyEntry{
		{AccessKey: "K1"}, // RPS=0 → unlimited
		{AccessKey: "K2", RequestsPerSecond: 5, Burst: 5},
	})
	if rl == nil {
		t.Fatalf("rl is nil, expected one configured key")
	}
	for i := 0; i < 1000; i++ {
		if !rl.allow("K1") {
			t.Fatalf("unlimited key K1 rejected at iteration %d", i)
		}
	}
}

// TestRateLimiterReturnsNilWhenAllUnlimited: when no key has a
// configured limit, newRateLimiter returns nil so the router can
// skip the check entirely (zero overhead for the common case).
func TestRateLimiterReturnsNilWhenAllUnlimited(t *testing.T) {
	rl := newRateLimiter([]KeyEntry{
		{AccessKey: "A"},
		{AccessKey: "B"},
		{AccessKey: "C", Burst: 100}, // Burst alone without RPS = no limit
	})
	if rl != nil {
		t.Fatalf("expected nil rateLimiter when no key has RPS, got %+v", rl)
	}
	// Nil receiver still admits.
	if !rl.allow("anything") {
		t.Fatalf("nil rl.allow returned false")
	}
}

// TestRateLimiterIsolatesKeys: throttling K1 must NOT impact K2.
// Cross-key bleeding would let one noisy tenant DoS others.
func TestRateLimiterIsolatesKeys(t *testing.T) {
	rl := newRateLimiter([]KeyEntry{
		{AccessKey: "K1", RequestsPerSecond: 1, Burst: 1},
		{AccessKey: "K2", RequestsPerSecond: 1, Burst: 1},
	})
	// Drain K1.
	if !rl.allow("K1") {
		t.Fatalf("K1 first request rejected")
	}
	if rl.allow("K1") {
		t.Fatalf("K1 second request right after drain accepted")
	}
	// K2 must still be at full burst.
	if !rl.allow("K2") {
		t.Fatalf("K2 first request rejected — K1's drain bled into K2's bucket")
	}
}

// TestRateLimiterBurstDefaultsToRPS: a key with RPS but no Burst
// gets Burst = RPS. Operators who set rps without thinking about
// burst still get a sensible default.
func TestRateLimiterBurstDefaultsToRPS(t *testing.T) {
	rl := newRateLimiter([]KeyEntry{
		{AccessKey: "K", RequestsPerSecond: 7, Burst: 0},
	})
	for i := 0; i < 7; i++ {
		if !rl.allow("K") {
			t.Fatalf("burst-default consume #%d rejected, want accept", i+1)
		}
	}
	if rl.allow("K") {
		t.Fatalf("consume #8 accepted — burst didn't default to RPS=7")
	}
}

// TestRateLimiterUnknownKeyAllowed: a verified access key that
// isn't in the limiter's bucket map (shouldn't happen normally —
// limiter is built from the same key list as auth — but we
// shouldn't crash). The contract: unknown key = allowed.
func TestRateLimiterUnknownKeyAllowed(t *testing.T) {
	rl := newRateLimiter([]KeyEntry{
		{AccessKey: "K", RequestsPerSecond: 1, Burst: 1},
	})
	for i := 0; i < 1000; i++ {
		if !rl.allow("UNKNOWN") {
			t.Fatalf("unknown key rejected at iteration %d", i)
		}
	}
}

// TestRateLimiterConcurrentAllow: hammer the limiter from many
// goroutines and verify the count of admitted requests doesn't
// exceed burst + (elapsed * rate). Without the per-bucket lock,
// concurrent consume() could double-count tokens and admit too
// many requests.
func TestRateLimiterConcurrentAllow(t *testing.T) {
	const (
		rps     = 100
		burst   = 100
		threads = 16
		perGo   = 1000
	)
	rl := newRateLimiter([]KeyEntry{
		{AccessKey: "K", RequestsPerSecond: rps, Burst: burst},
	})
	var wg sync.WaitGroup
	var admitted int64
	var mu sync.Mutex
	start := time.Now()
	for g := 0; g < threads; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := int64(0)
			for i := 0; i < perGo; i++ {
				if rl.allow("K") {
					local++
				}
			}
			mu.Lock()
			admitted += local
			mu.Unlock()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	maxAllowed := int64(burst) + int64(rps*int(elapsed)) + int64(rps) // +rps slack for ticker timing
	if admitted > maxAllowed {
		t.Errorf("admitted=%d, max-allowed under (burst=%d + %g sec * rps=%d + slack)=%d — concurrent over-admit",
			admitted, burst, elapsed, rps, maxAllowed)
	}
	if admitted < int64(burst) {
		t.Errorf("admitted=%d, want at least burst=%d", admitted, burst)
	}
}
