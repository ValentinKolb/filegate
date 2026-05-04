package domain

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func ownershipFromFileMeta(meta *FileMeta) *Ownership {
	if meta == nil {
		return nil
	}
	uid := int(meta.UID)
	gid := int(meta.GID)
	return &Ownership{
		UID:  &uid,
		GID:  &gid,
		Mode: fmt.Sprintf("%o", meta.Mode),
	}
}

func (s *Service) createAndWriteContent(parentID FileID, fileName string, body io.Reader, ownership *Ownership) (*FileMeta, error) {
	parentMeta, err := s.GetFile(parentID)
	if err != nil {
		return nil, err
	}
	if parentMeta.Type != "directory" {
		return nil, ErrInvalidArgument
	}

	parentAbs, err := s.ResolveAbsPath(parentID)
	if err != nil {
		return nil, err
	}
	targetAbs := filepath.Join(parentAbs, fileName)

	effectiveOwnership, err := s.effectiveOwnership(parentID, ownership)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeOwnership(effectiveOwnership)
	if err != nil {
		return nil, err
	}

	filePerm := os.FileMode(0o644)
	if normalized != nil && normalized.mode != nil {
		filePerm = *normalized.mode
	}

	newID, err := newID()
	if err != nil {
		return nil, err
	}
	if err := s.writeFileAtomic(targetAbs, body, filePerm, effectiveOwnership, &newID, true); err != nil {
		return nil, err
	}
	if err := s.syncSingle(targetAbs); err != nil {
		return nil, err
	}
	id, err := s.store.GetID(targetAbs)
	if err != nil {
		return nil, err
	}
	s.bus.Publish(Event{Type: EventCreated, ID: id, Path: targetAbs, At: time.Now()})
	// Auto V1 for the freshly-uploaded file (subject to the size floor).
	s.captureFirstVersion(id, targetAbs)
	return s.GetFile(id)
}

func syncFilePath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncDirPath(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func commitNoReplace(tmpPath, absPath string) error {
	if err := os.Link(tmpPath, absPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrConflict
		}
		return err
	}
	if err := os.Remove(tmpPath); err != nil {
		return err
	}
	return nil
}

func (s *Service) writeFileAtomic(absPath string, body io.Reader, filePerm os.FileMode, ownership *Ownership, preserveID *FileID, mustNotExist bool) error {
	absPath = filepath.Clean(strings.TrimSpace(absPath))
	if absPath == "" {
		return ErrInvalidArgument
	}

	linfo, err := os.Lstat(absPath)
	if err == nil {
		if linfo.Mode()&os.ModeSymlink != 0 {
			return ErrForbidden
		}
		if linfo.IsDir() {
			return ErrInvalidArgument
		}
		if mustNotExist {
			return ErrConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	parentDir := filepath.Dir(absPath)
	tmpFile, err := os.CreateTemp(parentDir, "."+filepath.Base(absPath)+".filegate-tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if filePerm == 0 {
		filePerm = 0o644
	}
	if err := tmpFile.Chmod(filePerm); err != nil {
		_ = tmpFile.Close()
		return err
	}
	buf := make([]byte, 128*1024)
	if _, err := io.CopyBuffer(tmpFile, body, buf); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	if preserveID != nil && !preserveID.IsZero() {
		if err := s.store.SetID(tmpPath, *preserveID); err != nil {
			return err
		}
	}
	if err := s.applyOwnership(tmpPath, ownership, false); err != nil {
		return err
	}
	if err := syncFilePath(tmpPath); err != nil {
		return err
	}

	if mustNotExist {
		if err := commitNoReplace(tmpPath, absPath); err != nil {
			return err
		}
	} else {
		if err := s.store.Rename(tmpPath, absPath); err != nil {
			return err
		}
	}
	finfo, err := os.Lstat(absPath)
	if err != nil {
		return err
	}
	if finfo.Mode()&os.ModeSymlink != 0 || !finfo.Mode().IsRegular() {
		return ErrForbidden
	}
	if preserveID != nil && !preserveID.IsZero() {
		got, err := s.store.GetID(absPath)
		if err != nil || got != *preserveID {
			if err := s.store.SetID(absPath, *preserveID); err != nil {
				return err
			}
		}
	}
	if s.dirSync != nil {
		if err := s.dirSync.Sync(parentDir); err != nil {
			return err
		}
	} else if err := syncDirPath(parentDir); err != nil {
		return err
	}

	cleanupTmp = false
	return nil
}
