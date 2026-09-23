package cumulite

import (
	"context"
	"time"

	"github.com/willove/cumudb/pkg/client"
)

// Port is the storage contract a lite CumuDB consumer runs on: the sixteen
// *client.Client methods the ask suite calls, nothing more. Method signatures
// are the client's, byte for byte — the client's query, KNN, index, change and
// health types are the wire contract, and errors satisfy client.IsNotFound so
// consumers keep one not-found idiom.
//
// Anything implementing Port drops into a consumer that was written against a
// remote server: *client.Client (HTTP) and *Engine (embedded Badger) are the
// two in this repository.
type Port interface {
	// Health reports reachability and engine identity.
	Health(ctx context.Context) (client.Health, error)

	// Documents -----------------------------------------------------------------
	// EnsureCollection declares a collection (idempotent; schemaless engines
	// treat it as a no-op).
	EnsureCollection(ctx context.Context, coll string) error

	// Insert stores documents as a batch. A document carrying _id (or _key)
	// adopts it as its identity; otherwise one is generated. A key that
	// already exists is an error — callers detect prior presence through it.
	Insert(ctx context.Context, coll string, documents []map[string]any) ([]string, error)

	// GetDocument returns one document or a not-found error.
	GetDocument(ctx context.Context, coll, id string) (map[string]any, error)

	// ReplaceDocument stores the document as the new full state; a missing
	// document is a not-found error.
	ReplaceDocument(ctx context.Context, coll, id string, document map[string]any) (map[string]any, error)

	// PatchDocument applies update operators ($set) and returns the stored
	// document.
	PatchDocument(ctx context.Context, coll, id string, update map[string]any) (map[string]any, error)

	// DeleteDocument removes a document, reporting whether it existed.
	DeleteDocument(ctx context.Context, coll, id string) (bool, error)

	// Query pages a collection: equality AND over filter fields, $in, $gte
	// (and the ordering operators), $or. Skip and Limit page in a stable
	// key order — an unsorted page with no stable order repeats and drops
	// documents.
	Query(ctx context.Context, coll string, query client.Query) (*client.QueryResult, error)

	// Key/value -----------------------------------------------------------------
	// KVPut stores a value, honouring a positive TTL.
	KVPut(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// KVGet returns a value or a not-found error.
	KVGet(ctx context.Context, key string) ([]byte, error)

	// KVDelete removes a key, reporting whether it existed.
	KVDelete(ctx context.Context, key string) (bool, error)

	// KVKeys lists keys under a prefix.
	KVKeys(ctx context.Context, prefix string, limit int) ([]string, error)

	// Vector --------------------------------------------------------------------
	// CreateIndexRequest declares a vector index (field, dims, metric, model).
	CreateIndexRequest(ctx context.Context, coll string, request client.IndexRequest) error

	// KNN returns the k nearest documents with their distances. The lite
	// engine scans exactly — no ANN structures — which is the honest cost at
	// corpus sizes up to the hundred-thousands.
	KNN(ctx context.Context, coll string, request client.KNNRequest) (*client.KNNResult, error)

	// Change log ----------------------------------------------------------------
	// SetChangelog turns recording of a collection's writes on or off.
	SetChangelog(ctx context.Context, coll string, enabled bool) error

	// Changes reads the records after a sequence cursor, oldest first. The
	// returned cursor is the last sequence returned, or the cursor the read
	// started from when nothing followed it.
	Changes(ctx context.Context, coll string, cursor uint64, limit int) (*client.ChangesPage, error)
}

// The compatibility claim, checked at compile time: the CumuDB HTTP client is
// a Port as it stands. A consumer written against it switches engines without
// touching a call site.
var _ Port = (*client.Client)(nil)
