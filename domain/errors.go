package domain

import "errors"

var (
	ErrNotFound            = errors.New("not found")
	ErrConflict            = errors.New("conflict")
	ErrInvalidArgument     = errors.New("invalid argument")
	ErrForbidden           = errors.New("forbidden")
	ErrInsufficientStorage = errors.New("insufficient storage")
)
