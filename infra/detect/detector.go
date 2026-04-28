package detect

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// EventType identifies the kind of filesystem change detected.
type EventType int

const (
	EventCreated EventType = iota
	EventChanged
	EventDeleted
	EventUnknown
)

// Event describes a single filesystem change detected by a Runner.
type Event struct {
	Type    EventType
	Base    string
	AbsPath string
	IsDir   bool
	Size    int64
	MtimeMS int64
}

// Runner is the interface for pluggable filesystem change detection backends.
type Runner interface {
	Start(context.Context)
	Events() <-chan []Event
	ForceRescan(context.Context) error
	Close()
	Name() string
}

// New creates a Runner for the specified backend ("auto", "poll", or "btrfs").
func New(backend string, basePaths []string, interval time.Duration) (Runner, error) {
	mode := strings.ToLower(strings.TrimSpace(backend))
	if mode == "" {
		mode = "auto"
	}

	switch mode {
	case "poll":
		return NewPoller(basePaths, interval), nil
	case "btrfs":
		ok, err := supportsBTRFS(context.Background(), basePaths)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("detection backend btrfs requested, but at least one base path is not btrfs")
		}
		return NewBTRFSDetector(basePaths, interval), nil
	case "auto":
		ok, err := supportsBTRFS(context.Background(), basePaths)
		if err != nil {
			return nil, err
		}
		if ok {
			return NewBTRFSDetector(basePaths, interval), nil
		}
		return NewPoller(basePaths, interval), nil
	default:
		return nil, fmt.Errorf("unknown detection backend: %s", mode)
	}
}
