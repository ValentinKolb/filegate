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
	Batch(fn func(Batch) error) error
	Close() error
}

// Batch is a write transaction against the index, used for bulk updates.
type Batch interface {
	PutEntity(entity Entity)
	PutChild(parentID FileID, name string, entry DirEntry)
	DelChild(parentID FileID, name string)
	DelEntity(id FileID)
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
}
