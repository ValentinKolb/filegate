package s3

import (
	"sync"
	"time"
)

// rateLimiter is a per-access-key token bucket. Each KeyEntry can
// pin its own RequestsPerSecond + Burst; the limiter is built once
// at NewHandler time and consulted on every request after SigV4
// verification.
//
// Algorithm: classic token bucket. Each bucket starts full
// (cap=Burst). Each successful request consumes 1 token. Tokens
// refill at RequestsPerSecond. When empty, the request is
// rejected. We don't queue or wait — the S3 spec calls for a
// SlowDown response and lets the client back off.
//
// A key with rps=0 (the default) is treated as unlimited and
// skips the bucket entirely. A key with rps>0 always also has a
// burst — if the operator sets rps without burst, burst defaults
// to rps (allowing one second's worth of bursty traffic).
type rateLimiter struct {
	// buckets is keyed by access-key. Built once at construction;
	// no further mutation, so lookups don't need a lock.
	buckets map[string]*tokenBucket
}

// newRateLimiter materializes a per-key bucket for every key with
// rps>0. Returns nil when no key has a limit configured — the
// caller can then skip the rate-limit check entirely.
func newRateLimiter(keys []KeyEntry) *rateLimiter {
	any := false
	for _, k := range keys {
		if k.RequestsPerSecond > 0 {
			any = true
			break
		}
	}
	if !any {
		return nil
	}
	buckets := make(map[string]*tokenBucket, len(keys))
	for _, k := range keys {
		if k.RequestsPerSecond <= 0 {
			continue
		}
		burst := k.Burst
		if burst <= 0 {
			burst = k.RequestsPerSecond
		}
		buckets[k.AccessKey] = newTokenBucket(float64(k.RequestsPerSecond), float64(burst))
	}
	return &rateLimiter{buckets: buckets}
}

// allow returns true when the request is admitted. An access key
// with no configured limit (or no rateLimiter at all) is always
// admitted. The lookup is O(1) via the map; the per-bucket lock
// is held only for the duration of one Now()-and-arithmetic.
func (rl *rateLimiter) allow(accessKey string) bool {
	if rl == nil {
		return true
	}
	b, ok := rl.buckets[accessKey]
	if !ok {
		return true
	}
	return b.consume()
}

// tokenBucket is the classic refill-and-take primitive. Decoupled
// from rateLimiter so it can be unit-tested in isolation, with
// fake clocks if needed.
type tokenBucket struct {
	mu sync.Mutex

	rate     float64 // tokens per second
	capacity float64 // burst limit
	tokens   float64 // current tokens (lazily updated on consume)
	last     time.Time
}

// newTokenBucket creates a bucket starting full (tokens = capacity).
// Both rate and capacity must be positive; the caller validates.
func newTokenBucket(rate, capacity float64) *tokenBucket {
	return &tokenBucket{
		rate:     rate,
		capacity: capacity,
		tokens:   capacity,
		last:     time.Now(),
	}
}

// consume tries to take one token. Returns true on success, false
// when the bucket is empty. Lazy-refills based on elapsed time.
func (b *tokenBucket) consume() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
