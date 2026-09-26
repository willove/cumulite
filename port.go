package cumulite

import (
	"context"
	"time"

	"github.com/willove/cumulite/contract"
)

// Port is the storage contract a lite CumuDB consumer runs on: the sixteen
// HTTP-client methods the ask suite calls, plus Subscribe — the changelog's
// blocking read, for a consumer that waits for writes instead of polling.
// Method signatures carry contract's types — this repository's fork of the
// HTTP client's wire contract — so the two engines stay separable projects
// and a consumer converts types at the boundary instead of importing an
// engine.
//
// Anything implementing Port drops into a consumer that was written against a
// remote server: the embedded Engine here, and (after converting request and
// result types) an HTTP client elsewhere.
type Port interface {
	// Health reports reachability and engine identity.
	Health(ctx context.Context) (contract.Health, error)

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
	Query(ctx context.Context, coll string, query contract.Query) (*contract.QueryResult, error)

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
	CreateIndexRequest(ctx context.Context, coll string, request contract.IndexRequest) error

	// KNN returns the k nearest documents with their distances. The lite
	// engine scans exactly — no ANN structures — which is the honest cost at
	// corpus sizes up to the hundred-thousands.
	KNN(ctx context.Context, coll string, request contract.KNNRequest) (*contract.KNNResult, error)

	// Change log ----------------------------------------------------------------
	// SetChangelog turns recording of a collection's writes on or off.
	SetChangelog(ctx context.Context, coll string, enabled bool) error

	// Changes reads the records after a sequence cursor, oldest first. The
	// returned cursor is the last sequence returned, or the cursor the read
	// started from when nothing followed it.
	Changes(ctx context.Context, coll string, cursor uint64, limit int) (*contract.ChangesPage, error)

	// Subscribe is the changelog's blocking read: it waits until a record
	// follows cursor (or the changelog is off, or ctx ends) and returns one
	// page. The engine holds no subscriber state — the caller owns the
	// cursor, so waiting, restarting and switching readers are all the same
	// loop.
	Subscribe(ctx context.Context, coll string, cursor uint64, limit int) (*contract.ChangesPage, error)
}

// The engine is the contract.
var _ Port = (*Engine)(nil)

// Port above is the wire contract; what follows are optional capability
// interfaces the embedded Engine also satisfies. A consumer opts in per
// capability with a type assertion, so a remote adapter that implements only
// Port keeps compiling and a consumer can branch on the presence of a
// capability rather than on the engine kind:
//
//	if sp, ok := port.(cumulite.StructPort); ok { ... }
//
// They exist because of one incident: a consumer hand-maintained a
// struct→map translation table, a new field missed the table, and the loss
// reached storage silently — reads decode the whole document, so only the
// missing field's readers could ever notice. The capabilities close that
// class of bug three ways: don't translate (StructPort), audit every write
// against a declared shape (ShapePort), or prove the round-trip after the
// fact (DocVerifier).

// StructPort is the typed-write capability: store whole Go structs, json tags
// naming the fields, so no hand-built map stands between the domain struct
// and storage and a new field cannot be dropped by a translation table that
// forgot it.
type StructPort interface {
	// InsertStructs marshals the values (tags are the field names) and
	// stores them through the same path as Insert.
	InsertStructs(ctx context.Context, coll string, docs []any) ([]string, error)
	// ReplaceStruct replaces one document from a struct value.
	ReplaceStruct(ctx context.Context, coll, id string, doc any) (map[string]any, error)
}

// ShapePort is the shape-audit capability: declare a collection's canonical
// document shape (a zero value of the domain struct) and every write to the
// collection is checked against its json tags — required tags absent from the
// document, document keys no tag claims. Strict mode fails such a write;
// lenient mode remembers the finding for later inspection.
type ShapePort interface {
	// SetCollectionShape declares or refreshes a collection's shape.
	SetCollectionShape(ctx context.Context, coll string, shape any) error
	// SetShapeStrict arms or disarms failing writes on shape violations.
	SetShapeStrict(ctx context.Context, coll string, strict bool) error
	// ShapeReport audits any document against the declared shape without
	// writing it — the ruler for parity tests and ops checks.
	ShapeReport(coll string, doc map[string]any) (ShapeAudit, error)
	// LastShapeAudit reports the most recent non-empty finding this engine
	// process recorded for the collection.
	LastShapeAudit(coll string) (ShapeAudit, bool)
}

// DocVerifier is the round-trip-proof capability: read back what storage
// actually holds for one document and diff it against the original — lost
// keys, gained keys, changed values. It needs no declared shape, which makes
// it the one-call reproduction of a shape audit's finding in tests and ops
// checks.
type DocVerifier interface {
	VerifyDoc(ctx context.Context, coll, id string, original any) ([]string, error)
}

var (
	_ StructPort  = (*Engine)(nil)
	_ ShapePort   = (*Engine)(nil)
	_ DocVerifier = (*Engine)(nil)
)
