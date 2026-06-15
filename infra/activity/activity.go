// Package activity provides the in-process activity log used by the
// admin UI and lightweight operator introspection.
package activity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ActorKind string
type Outcome string

const (
	ActorSystem    ActorKind = "system"
	ActorBearer    ActorKind = "bearer_token"
	ActorS3Key     ActorKind = "s3_key"
	ActorSignedURL ActorKind = "signed_url"

	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
	OutcomeSkipped   Outcome = "skipped"
)

type Actor struct {
	Kind           ActorKind `json:"kind"`
	ID             string    `json:"id"`
	Label          string    `json:"label,omitempty"`
	DelegatedActor string    `json:"delegatedActor,omitempty"`
}

type Target struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Path string `json:"path,omitempty"`
}

type Event struct {
	ID         string         `json:"id"`
	At         time.Time      `json:"at"`
	Actor      Actor          `json:"actor"`
	Operation  string         `json:"operation"`
	Outcome    Outcome        `json:"outcome"`
	Target     *Target        `json:"target,omitempty"`
	DurationMS int64          `json:"durationMs,omitempty"`
	RequestID  string         `json:"requestId,omitempty"`
	Error      string         `json:"error,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type Ring struct {
	mu       sync.RWMutex
	events   []Event
	next     int
	full     bool
	capacity int
	seq      atomic.Uint64
}

func NewRing(size int) *Ring {
	if size <= 0 {
		return nil
	}
	return &Ring{events: make([]Event, size), capacity: size}
}

func (r *Ring) Capacity() int {
	if r == nil {
		return 0
	}
	return r.capacity
}

func (r *Ring) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.full {
		return r.capacity
	}
	return r.next
}

func (r *Ring) Record(event Event) {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	if event.At.IsZero() {
		event.At = now
	}
	if event.ID == "" {
		event.ID = fmt.Sprintf("%x-%x", event.At.UnixNano(), r.seq.Add(1))
	}
	if event.Actor.Kind == "" {
		event.Actor = Actor{Kind: ActorSystem, ID: "filegate"}
	}
	if event.Outcome == "" {
		event.Outcome = OutcomeSucceeded
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.events[r.next] = event
	r.next = (r.next + 1) % r.capacity
	if r.next == 0 {
		r.full = true
	}
}

func (r *Ring) Snapshot(limit int) []Event {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := r.next
	if r.full {
		count = r.capacity
	}
	if limit <= 0 || limit > count {
		limit = count
	}
	out := make([]Event, 0, limit)
	for i := 0; i < limit; i++ {
		idx := r.next - 1 - i
		if idx < 0 {
			idx += r.capacity
		}
		if !r.full && idx >= r.next {
			break
		}
		out = append(out, r.events[idx])
	}
	return out
}

func CredentialID(prefix, secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return prefix + ":" + hex.EncodeToString(sum[:])[:16]
}

func CleanActorLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(value)
	if len(value) > 128 {
		value = value[:128]
	}
	return value
}
