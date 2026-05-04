package domain

import (
	"io"
	"os"
)

// Index is the port interface for metadata storage and retrieval.
type Index interface {
	GetEntity(id FileID) (*Entity, error)
	LookupChild(parentID FileID, name string) (*DirEntry, error)
	ListChildren(parentID FileID, after string, limit int) ([]DirEntry, error)
	ListEntities() ([]Entity, error)
	ForEachEntity(func(Entity) error) error
	GetVersion(fileID FileID, versionID VersionID) (*VersionMeta, error)
	ListVersions(fileID FileID, after VersionID, limit int) ([]VersionMeta, error)
	LatestVersionTimestamp(fileID FileID) (int64, error)
	MarkVersionsDeleted(fileID FileID, deletedAt int64) (int, error)
	Batch(fn func(Batch) error) error
	Close() error
}

// Batch is a write transaction against the index, used for bulk updates.
type Batch interface {
	PutEntity(entity Entity)
	PutChild(parentID FileID, name string, entry DirEntry)
	DelChild(parentID FileID, name string)
	DelEntity(id FileID)
	PutVersion(meta VersionMeta)
	DelVersion(fileID FileID, versionID VersionID)
}

// Store is the port interface for filesystem I/O operations.
type Store interface {
	Abs(path string) (string, error)
	Stat(path string) (os.FileInfo, error)
	ReadDir(path string) ([]os.DirEntry, error)
	MkdirAll(path string, perm os.FileMode) error
	RemoveAll(path string) error
	Remove(path string) error
	Rename(oldPath, newPath string) error
	OpenRead(path string) (io.ReadCloser, error)
	OpenWrite(path string, perm os.FileMode) (io.WriteCloser, error)
	SetID(path string, id FileID) error
	GetID(path string) (FileID, error)
	// CloneFile copies srcPath to dstPath using FICLONE on btrfs (cheap,
	// constant-time) or a byte copy fallback. dstPath must not exist;
	// callers create the parent directory first. Returns true when the
	// reflink fast-path was used. Used by the versioning subsystem to
	// snapshot file bytes without paying double the storage on btrfs.
	CloneFile(srcPath, dstPath string) (reflinked bool, err error)
}
