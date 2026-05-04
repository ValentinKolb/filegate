package domain

import "errors"

var (
	ErrNotFound            = errors.New("not found")
	ErrConflict            = errors.New("conflict")
	ErrInvalidArgument     = errors.New("invalid argument")
	ErrForbidden           = errors.New("forbidden")
	ErrInsufficientStorage = errors.New("insufficient storage")
	// ErrUnsupportedFS is returned by versioning operations against a
	// path / mount whose filesystem cannot host the feature (no btrfs
	// reflinks, or versioning explicitly disabled in config).
	ErrUnsupportedFS = errors.New("unsupported filesystem for versioning")
)
