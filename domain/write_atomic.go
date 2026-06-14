package domain

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
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

	parentVP, err := s.VirtualPath(parentID)
	if err != nil {
		return nil, err
	}
	pmount, prel, vpOK := splitVirtualPath(parentVP)
	if !vpOK {
		return nil, ErrInvalidArgument
	}
	var leafRel string
	if prel == "" {
		leafRel = fileName
	} else {
		leafRel = prel + "/" + fileName
	}
	release := s.pathLocks.AcquirePoint(pathLockKey(pmount, leafRel))
	defer release()

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
	hashes, err := s.writeFileAtomic(targetAbs, body, filePerm, effectiveOwnership, &newID, true)
	if err != nil {
		return nil, err
	}
	if err := s.syncSingleAfterLocalWrite(targetAbs, hashes); err != nil {
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

// writeFileAtomic writes body to absPath via the standard tmp+rename pattern
// and computes content hashes during the same body→disk copy.
func (s *Service) writeFileAtomic(absPath string, body io.Reader, filePerm os.FileMode, ownership *Ownership, preserveID *FileID, mustNotExist bool) (ContentHashes, error) {
	absPath = filepath.Clean(strings.TrimSpace(absPath))
	if absPath == "" {
		return ContentHashes{}, ErrInvalidArgument
	}

	linfo, err := os.Lstat(absPath)
	if err == nil {
		if linfo.Mode()&os.ModeSymlink != 0 {
			return ContentHashes{}, ErrForbidden
		}
		if linfo.IsDir() {
			return ContentHashes{}, ErrInvalidArgument
		}
		if mustNotExist {
			return ContentHashes{}, ErrConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ContentHashes{}, err
	}

	parentDir := filepath.Dir(absPath)
	tmpFile, err := os.CreateTemp(parentDir, "."+filepath.Base(absPath)+".filegate-tmp-*")
	if err != nil {
		return ContentHashes{}, err
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
		return ContentHashes{}, err
	}

	// io.MultiWriter fans each Write to the file and both hashes, so we
	// never re-read the body to fingerprint it.
	md5Hash := md5.New()
	shaHash := sha256.New()
	dst := io.MultiWriter(tmpFile, hash.Hash(md5Hash), hash.Hash(shaHash))
	buf := make([]byte, 128*1024)
	if _, err := io.CopyBuffer(dst, body, buf); err != nil {
		_ = tmpFile.Close()
		return ContentHashes{}, err
	}
	hashes := ContentHashes{
		MD5Hex: hex.EncodeToString(md5Hash.Sum(nil)),
		SHA256: "sha256:" + hex.EncodeToString(shaHash.Sum(nil)),
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return ContentHashes{}, err
	}
	if err := tmpFile.Close(); err != nil {
		return ContentHashes{}, err
	}

	if preserveID != nil && !preserveID.IsZero() {
		if err := s.store.SetID(tmpPath, *preserveID); err != nil {
			return ContentHashes{}, err
		}
	}
	if err := s.applyOwnership(tmpPath, ownership, false); err != nil {
		return ContentHashes{}, err
	}
	if err := syncFilePath(tmpPath); err != nil {
		return ContentHashes{}, err
	}

	if mustNotExist {
		if err := commitNoReplace(tmpPath, absPath); err != nil {
			return ContentHashes{}, err
		}
	} else {
		if err := s.store.Rename(tmpPath, absPath); err != nil {
			return ContentHashes{}, err
		}
	}
	finfo, err := os.Lstat(absPath)
	if err != nil {
		return ContentHashes{}, err
	}
	if finfo.Mode()&os.ModeSymlink != 0 || !finfo.Mode().IsRegular() {
		return ContentHashes{}, ErrForbidden
	}
	if preserveID != nil && !preserveID.IsZero() {
		got, err := s.store.GetID(absPath)
		if err != nil || got != *preserveID {
			if err := s.store.SetID(absPath, *preserveID); err != nil {
				return ContentHashes{}, err
			}
		}
	}
	if s.dirSync != nil {
		if err := s.dirSync.Sync(parentDir); err != nil {
			return ContentHashes{}, err
		}
	} else if err := syncDirPath(parentDir); err != nil {
		return ContentHashes{}, err
	}

	cleanupTmp = false
	return hashes, nil
}
