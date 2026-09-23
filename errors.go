package cumulite

import (
	"errors"
	"fmt"

	"github.com/willove/cumudb/pkg/client"
)

// ErrNotFound is the client's sentinel, re-exported so engine users share one
// idiom with server users: errors.Is(err, ErrNotFound) and client.IsNotFound
// both hold for every missing document, key or collection the engine reports.
var ErrNotFound = client.ErrNotFound

func notFoundf(format string, args ...any) error {
	return fmt.Errorf("cumulite: %s: %w", fmt.Sprintf(format, args...), ErrNotFound)
}

// IsNotFound reports whether err represents a missing resource.
func IsNotFound(err error) bool { return client.IsNotFound(err) }

var (
	errNoVector   = errors.New("cumulite: no vector index on field")
	errDimsMismat = errors.New("cumulite: vector dimension mismatch")
)
