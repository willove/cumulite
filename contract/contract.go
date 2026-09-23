package contract

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrNotFound is the sentinel every "missing resource" error wraps: a document,
// a key, or a collection that was never declared. errors.Is(err, ErrNotFound)
// is the one idiom both engines' consumers share.
var ErrNotFound = errors.New("cumulite: not found")

// APIError is a structured error. The embedded engine never returns one — its
// failures are wrapped sentinels — but the type travels with the contract so a
// consumer written against the HTTP engine keeps classifying 404s the same way.
type APIError struct {
	Status  int            `json:"-"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("cumulite: %s (HTTP %d): %s", e.Code, e.Status, e.Message)
}

// IsNotFound reports whether err represents a missing resource.
func IsNotFound(err error) bool {
	if errors.Is(err, ErrNotFound) {
		return true
	}
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// Health is the engine's self-report.
type Health struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	Backend   string `json:"backend"`
	InMemory  bool   `json:"inMemory"`
	UptimeSec int    `json:"uptimeSec"`
}

// Query pages a collection. The lite engine honours Filter, Skip and Limit and
// refuses Sort and Projection (see Engine.Query) — a page whose order shifts
// between reads repeats and drops documents, so the engine walks the keyspace
// in key order instead.
type Query struct {
	Filter     any
	Sort       any
	Projection any
	Skip       int
	Limit      int
}

// QueryResult is one page: the documents, how many the scan examined and
// matched, and the plan that produced the page (so a reader can tell an exact
// scan from an index walk).
type QueryResult struct {
	Documents []map[string]any `json:"documents"`
	Count     int              `json:"count"`
	Plan      string           `json:"plan"`
	Examined  int              `json:"examined"`
	Matched   int              `json:"matched"`
	Sorted    bool             `json:"sorted"`
	Skip      int              `json:"skip"`
	Limit     int              `json:"limit"`
}

// IndexRequest declares an index. The lite engine answers Type "vector" only;
// every other family (persistent, text, graph, geo, time series) is refused by
// name rather than silently ignored.
type IndexRequest struct {
	Name    string   `json:"name,omitempty"`
	Field   string   `json:"field,omitempty"`
	Fields  []string `json:"fields,omitempty"`
	Unique  bool     `json:"unique,omitempty"`
	Include []string `json:"include,omitempty"`
	Type    string   `json:"type,omitempty"`
	Dims    int      `json:"dims,omitempty"`
	Metric  string   `json:"metric,omitempty"`
	// Model labels the embedding model behind a vector index.
	Model string `json:"model,omitempty"`
	// Ann asks a vector index to build its search structure, and NList names
	// how many buckets it gets; NProbe tunes how many of them a query probes.
	// Either Ann or NList triggers the build.
	Ann    bool `json:"ann,omitempty"`
	NList  int  `json:"nlist,omitempty"`
	NProbe int  `json:"nprobe,omitempty"`
	// Hnsw asks for a graph-structured vector index instead of buckets; M,
	// EfConstruction and EfSearch describe the graph (zero resolves the
	// defaults at creation). NList is refused alongside Hnsw.
	Hnsw           bool `json:"hnsw,omitempty"`
	M              int  `json:"m,omitempty"`
	EfConstruction int  `json:"efc,omitempty"`
	EfSearch       int  `json:"efs,omitempty"`
	// Tokenizer, K1 and B describe a text index.
	Tokenizer string  `json:"tokenizer,omitempty"`
	K1        float64 `json:"k1,omitempty"`
	B         float64 `json:"b,omitempty"`
	// Precision and Datum describe a geo index: the geohash precision its
	// entries carry and the coordinate system its field's points are in.
	Precision int    `json:"precision,omitempty"`
	Datum     string `json:"datum,omitempty"`
}

// KNNRequest asks for the k nearest documents to a vector. Filter, when set,
// keeps only the documents matching it — the engine applies it before ranking
// so a retired source never reaches the answer.
type KNNRequest struct {
	Field  string    `json:"field"`
	Vector []float64 `json:"vector"`
	K      int       `json:"k"`
	Metric string    `json:"metric,omitempty"`
	Filter any       `json:"filter,omitempty"`
	Index  string    `json:"index,omitempty"`
}

// KNNResult is the answer: the documents and the parallel distances behind
// their order, the access path that produced them (plan), and how much of the
// index the scan examined.
type KNNResult struct {
	Documents []map[string]any `json:"documents"`
	Distances []float64        `json:"distances"`
	Count     int              `json:"count"`
	Field     string           `json:"field"`
	Metric    string           `json:"metric"`
	Plan      string           `json:"plan"`
	Examined  int              `json:"examined"`
	Matched   int              `json:"matched"`
}

// ChangeRecord is one recorded write. Sequence orders the log, Op names the
// operation, Created tells an insert from an update of the same identity.
type ChangeRecord struct {
	Sequence uint64    `json:"sequence"`
	Op       string    `json:"op"`
	ID       string    `json:"id"`
	Created  bool      `json:"created,omitempty"`
	At       time.Time `json:"at"`
}

// ChangesPage is one read of the log after a cursor.
type ChangesPage struct {
	Changes []ChangeRecord `json:"changes"`
	Count   int            `json:"count"`
	// Cursor is where the next read resumes: the last sequence returned, or the
	// cursor the read started from when nothing followed it.
	Cursor uint64 `json:"cursor"`
	// Enabled reports whether the collection is currently recording writes.
	Enabled bool `json:"enabled"`
}
