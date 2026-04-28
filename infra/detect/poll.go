package detect

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type fileTrack struct {
	size       int64
	mtimeMS    int64
	checkEvery uint8
}

// Poller detects filesystem changes by periodically scanning directories with readdir/lstat.
type Poller struct {
	basePaths []string
	interval  time.Duration

	events chan []Event

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu         sync.Mutex
	knownDirs  map[string]int64
	knownFiles map[string]fileTrack
	cycle      uint64
}

// NewPoller creates a polling-based change detector for the given paths.
func NewPoller(basePaths []string, interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	clean := make([]string, 0, len(basePaths))
	for _, p := range basePaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		clean = append(clean, filepath.Clean(p))
	}
	return &Poller{
		basePaths:  clean,
		interval:   interval,
		events:     make(chan []Event, 64),
		knownDirs:  make(map[string]int64, 4096),
		knownFiles: make(map[string]fileTrack, 8192),
	}
}

func (p *Poller) Name() string {
	return "poll"
}

func (p *Poller) Events() <-chan []Event {
	return p.events
}

func (p *Poller) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.initialize()

		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				batch := p.poll()
				if len(batch) == 0 {
					continue
				}
				select {
				case p.events <- batch:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

func (p *Poller) ForceRescan(ctx context.Context) error {
	p.mu.Lock()
	for dir := range p.knownDirs {
		p.knownDirs[dir] = 0
	}
	p.mu.Unlock()

	batch := make([]Event, 0, len(p.basePaths))
	for _, basePath := range p.basePaths {
		batch = append(batch, Event{Type: EventUnknown, Base: basePath, AbsPath: basePath, IsDir: true})
	}
	select {
	case p.events <- batch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Poller) Close() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
	close(p.events)
}

func (p *Poller) initialize() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, basePath := range p.basePaths {
		if err := filepath.WalkDir(basePath, func(current string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if info.IsDir() {
				p.knownDirs[current] = info.ModTime().UnixMilli()
				return nil
			}
			p.knownFiles[current] = fileTrack{size: info.Size(), mtimeMS: info.ModTime().UnixMilli(), checkEvery: 1}
			return nil
		}); err != nil {
			log.Printf("[filegate] poll detector init walk failed for %q: %v", basePath, err)
		}
	}
}

func (p *Poller) poll() []Event {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.cycle++
	batch := make([]Event, 0, 128)

	dirtyDirs := make(map[string]struct{}, 64)
	deletedDirs := make(map[string]struct{}, 16)

	for dirPath, lastMtime := range p.knownDirs {
		info, err := os.Lstat(dirPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				deletedDirs[dirPath] = struct{}{}
				continue
			}
			continue
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			deletedDirs[dirPath] = struct{}{}
			continue
		}
		currentMtime := info.ModTime().UnixMilli()
		if currentMtime != lastMtime {
			dirtyDirs[dirPath] = struct{}{}
			p.knownDirs[dirPath] = currentMtime
		}
	}

	dirtyDirList := make([]string, 0, len(dirtyDirs))
	for dirPath := range dirtyDirs {
		dirtyDirList = append(dirtyDirList, dirPath)
	}
	sort.Strings(dirtyDirList)
	for _, dirPath := range dirtyDirList {
		batch = append(batch, p.scanDirectory(dirPath)...)
	}

	for filePath, track := range p.knownFiles {
		if isInDeletedDir(filePath, deletedDirs) {
			continue
		}
		dirPath := filepath.Dir(filePath)
		if _, dirty := dirtyDirs[dirPath]; dirty {
			continue
		}
		if track.checkEvery == 0 {
			track.checkEvery = 1
		}
		if p.cycle%uint64(track.checkEvery) != 0 {
			continue
		}

		info, err := os.Lstat(filePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				base := findBasePath(filePath, p.basePaths)
				batch = append(batch, Event{Type: EventDeleted, Base: base, AbsPath: filePath, IsDir: false})
				delete(p.knownFiles, filePath)
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
			base := findBasePath(filePath, p.basePaths)
			batch = append(batch, Event{Type: EventDeleted, Base: base, AbsPath: filePath, IsDir: false})
			delete(p.knownFiles, filePath)
			continue
		}

		mtime := info.ModTime().UnixMilli()
		size := info.Size()
		if size != track.size || mtime != track.mtimeMS {
			track.size = size
			track.mtimeMS = mtime
			track.checkEvery = 1
			p.knownFiles[filePath] = track
			base := findBasePath(filePath, p.basePaths)
			batch = append(batch, Event{Type: EventChanged, Base: base, AbsPath: filePath, IsDir: false, Size: size, MtimeMS: mtime})
			continue
		}

		if track.checkEvery < 64 {
			track.checkEvery *= 2
			if track.checkEvery > 64 {
				track.checkEvery = 64
			}
			p.knownFiles[filePath] = track
		}
	}

	for dirPath := range deletedDirs {
		base := findBasePath(dirPath, p.basePaths)
		batch = append(batch, Event{Type: EventDeleted, Base: base, AbsPath: dirPath, IsDir: true})
		p.removeDirSubtree(dirPath)
	}

	return dedupeEvents(batch)
}

func (p *Poller) scanDirectory(absDir string) []Event {
	entries, err := os.ReadDir(absDir)
	if err != nil {
		base := findBasePath(absDir, p.basePaths)
		return []Event{{Type: EventUnknown, Base: base, AbsPath: absDir, IsDir: true}}
	}

	base := findBasePath(absDir, p.basePaths)
	onDisk := make(map[string]os.DirEntry, len(entries))
	for _, entry := range entries {
		onDisk[entry.Name()] = entry
	}

	events := make([]Event, 0, len(entries)+8)

	for filePath := range p.knownFiles {
		if filepath.Dir(filePath) != absDir {
			continue
		}
		name := filepath.Base(filePath)
		if _, exists := onDisk[name]; exists {
			continue
		}
		events = append(events, Event{Type: EventDeleted, Base: base, AbsPath: filePath, IsDir: false})
		delete(p.knownFiles, filePath)
	}

	for dirPath := range p.knownDirs {
		if filepath.Dir(dirPath) != absDir {
			continue
		}
		name := filepath.Base(dirPath)
		if _, exists := onDisk[name]; exists {
			continue
		}
		events = append(events, Event{Type: EventDeleted, Base: base, AbsPath: dirPath, IsDir: true})
		p.removeDirSubtree(dirPath)
	}

	for _, entry := range entries {
		name := entry.Name()
		absPath := filepath.Join(absDir, name)
		info, err := os.Lstat(absPath)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		if info.IsDir() {
			mtime := info.ModTime().UnixMilli()
			if _, exists := p.knownDirs[absPath]; !exists {
				events = append(events, Event{Type: EventCreated, Base: base, AbsPath: absPath, IsDir: true, MtimeMS: mtime})
			}
			p.knownDirs[absPath] = mtime
			continue
		}

		track, exists := p.knownFiles[absPath]
		mtime := info.ModTime().UnixMilli()
		size := info.Size()
		if !exists {
			events = append(events, Event{Type: EventCreated, Base: base, AbsPath: absPath, IsDir: false, Size: size, MtimeMS: mtime})
			p.knownFiles[absPath] = fileTrack{size: size, mtimeMS: mtime, checkEvery: 1}
			continue
		}
		if track.size != size || track.mtimeMS != mtime {
			events = append(events, Event{Type: EventChanged, Base: base, AbsPath: absPath, IsDir: false, Size: size, MtimeMS: mtime})
			track.size = size
			track.mtimeMS = mtime
			track.checkEvery = 1
			p.knownFiles[absPath] = track
		}
	}

	return events
}

func (p *Poller) removeDirSubtree(dirPath string) {
	prefix := dirPath + string(os.PathSeparator)
	for known := range p.knownDirs {
		if known == dirPath || strings.HasPrefix(known, prefix) {
			delete(p.knownDirs, known)
		}
	}
	for known := range p.knownFiles {
		if strings.HasPrefix(known, prefix) {
			delete(p.knownFiles, known)
		}
	}
}

func isInDeletedDir(path string, deletedDirs map[string]struct{}) bool {
	for dir := range deletedDirs {
		if path == dir || strings.HasPrefix(path, dir+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func findBasePath(absPath string, bases []string) string {
	best := ""
	for _, base := range bases {
		if absPath == base || strings.HasPrefix(absPath, base+string(os.PathSeparator)) {
			if len(base) > len(best) {
				best = base
			}
		}
	}
	return best
}

func dedupeEvents(events []Event) []Event {
	if len(events) == 0 {
		return nil
	}
	priority := map[EventType]int{
		EventUnknown: 4,
		EventDeleted: 3,
		EventChanged: 2,
		EventCreated: 1,
	}
	m := make(map[string]Event, len(events))
	for _, ev := range events {
		if ev.AbsPath == "" {
			continue
		}
		key := ev.AbsPath
		if cur, exists := m[key]; exists {
			if priority[ev.Type] >= priority[cur.Type] {
				m[key] = ev
			}
			continue
		}
		m[key] = ev
	}
	out := make([]Event, 0, len(m))
	for _, ev := range m {
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AbsPath < out[j].AbsPath })
	return out
}
