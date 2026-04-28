package domain

import (
	"fmt"
	"strings"
)

// ConflictMode controls how Filegate handles a name collision when creating
// or replacing a node.
//
//   - ConflictError     — fail with ErrConflict (default everywhere).
//   - ConflictOverwrite — replace existing target. For directories (Transfer)
//     this means recursive delete + recreate; for files this means atomic
//     replace.
//   - ConflictRename    — pick the next free name with the suffix scheme
//     "<stem>-<NN><ext>" via makeUniquePath.
//   - ConflictSkip      — only valid for mkdir: if a directory with the same
//     name exists, return it unchanged. A file with the same name still
//     fails (we cannot "skip" a type mismatch).
type ConflictMode string

const (
	ConflictError     ConflictMode = "error"
	ConflictOverwrite ConflictMode = "overwrite"
	ConflictRename    ConflictMode = "rename"
	ConflictSkip      ConflictMode = "skip"
)

// ConflictAllowed encodes which modes a caller is permitted to request at a
// given API surface. Skip is mkdir-only because it has no clean semantics
// for file content (we'd silently drop the request body).
type ConflictAllowed struct {
	allowOverwrite bool
	allowSkip      bool
}

// FileConflictModes is the allowed set for endpoints that write file content
// (PUT /v1/paths, chunked upload start, ReplaceFile, Transfer).
var FileConflictModes = ConflictAllowed{allowOverwrite: true, allowSkip: false}

// MkdirConflictModes is the allowed set for mkdir endpoints. Overwrite is
// intentionally NOT allowed: replacing a directory recursively is a Transfer
// operation, not a mkdir one.
var MkdirConflictModes = ConflictAllowed{allowOverwrite: false, allowSkip: true}

// ParseConflictMode parses a user-supplied string against an allowed set.
// Empty input returns ConflictError (the safe default).
func ParseConflictMode(s string, allowed ConflictAllowed) (ConflictMode, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ConflictError, nil
	}
	mode := ConflictMode(s)
	switch mode {
	case ConflictError, ConflictRename:
		return mode, nil
	case ConflictOverwrite:
		if !allowed.allowOverwrite {
			return "", fmt.Errorf("%w: onConflict=overwrite not allowed here", ErrInvalidArgument)
		}
		return mode, nil
	case ConflictSkip:
		if !allowed.allowSkip {
			return "", fmt.Errorf("%w: onConflict=skip not allowed here", ErrInvalidArgument)
		}
		return mode, nil
	default:
		return "", fmt.Errorf("%w: unknown onConflict mode %q", ErrInvalidArgument, s)
	}
}
