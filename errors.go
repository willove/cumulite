package cumulite

import (
	"errors"
	"fmt"

	"github.com/willove/cumulite/contract"
)

// ErrNotFound is the contract's sentinel, re-exported so engine users share one
// idiom with server users: errors.Is(err, ErrNotFound) and contract.IsNotFound
// both hold for every missing document, key or collection the engine reports.
var ErrNotFound = contract.ErrNotFound

// ErrDuplicate marks a batch insert whose identity was already stored. The
// server answers the same collision with a 409, and callers of the embedded
// engine get a classifiable error instead of a message they must string-match.
var ErrDuplicate = errors.New("cumulite: duplicate document")

// ErrUnsupported marks a request field the lite engine refuses rather than
// ignores: Query.Sort, dotted projection paths. A silently dropped sort
// reorders what the caller believes it asked for, which is worse than a loud
// failure.
var ErrUnsupported = errors.New("cumulite: unsupported request field")

// ErrShapeViolation marks a write whose document misses keys the collection's
// declared shape requires or carries keys no shape tag claims — the drift a
// hand-maintained struct→map translation table produces when it forgets a new
// field. Strict mode (SetShapeStrict) fails such writes with this sentinel so
// callers can classify them; lenient mode stores the document and records the
// finding for LastShapeAudit instead.
var ErrShapeViolation = errors.New("cumulite: shape violation")

func notFoundf(format string, args ...any) error {
	return fmt.Errorf("cumulite: %s: %w", fmt.Sprintf(format, args...), ErrNotFound)
}

// IsNotFound reports whether err represents a missing resource.
func IsNotFound(err error) bool { return contract.IsNotFound(err) }

var (
	errNoVector   = errors.New("cumulite: no vector index on field")
	errDimsMismat = errors.New("cumulite: vector dimension mismatch")
)
