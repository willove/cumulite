package cumulite

import (
	"context"
	"errors"
	"testing"

	"github.com/willove/cumulite/contract"
)

// The lite engine's creed is "refuse rather than ignore". These tests pin the
// three places where that creed was only a comment: a request field the engine
// cannot honour must fail loudly, a filter the caller passes must actually
// filter, and a cancelled context must stop a scan.

func TestQueryRefusesSortAndDottedProjection(t *testing.T) {
	e := openEngine(t)
	mustEnsure(t, e, "c")
	if _, err := e.Insert(context.Background(), "c", []map[string]any{{"_id": "a", "n": 1.0}}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if _, err := e.Query(context.Background(), "c", contract.Query{Sort: map[string]any{"n": -1}}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Sort: err = %v, want ErrUnsupported", err)
	}
	// Top-level projection is honoured; a dotted path is the part that needs
	// a projection planner the lite engine has none of, and is refused.
	if _, err := e.Query(context.Background(), "c", contract.Query{Projection: map[string]any{"a.b": 1}}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("dotted Projection: err = %v, want ErrUnsupported", err)
	}
	// The supported fields still work.
	page, err := e.Query(context.Background(), "c", contract.Query{
		Filter:     map[string]any{"n": 1.0},
		Projection: map[string]any{"n": 1, "_id": 0},
		Skip:       0,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("filter page: %v", err)
	}
	if len(page.Documents) != 1 || len(page.Documents[0]) != 1 || page.Documents[0]["n"] != 1.0 {
		t.Fatalf("projected page = %+v", page.Documents)
	}
}

func TestKNNFilterExcludesNonMatching(t *testing.T) {
	e := openEngine(t)
	mustEnsure(t, e, "c")
	// The index must exist before the write: vectors are mirrored at write
	// time, so a later-created index has no vectors to scan.
	if err := e.CreateIndexRequest(context.Background(), "c", contract.IndexRequest{
		Type: "vector", Field: "v", Dims: 3, Metric: "cosine",
	}); err != nil {
		t.Fatalf("index: %v", err)
	}
	docs := []map[string]any{
		{"_id": "live-far", "v": []float64{0, 0, 1}, "status": "live"},
		{"_id": "live-near", "v": []float64{1, 0, 0}, "status": "live"},
		{"_id": "retired-nearest", "v": []float64{1, 0, 0}, "status": "retired"},
	}
	if _, err := e.Insert(context.Background(), "c", docs); err != nil {
		t.Fatalf("insert: %v", err)
	}

	unfiltered, err := e.KNN(context.Background(), "c", contract.KNNRequest{Field: "v", Vector: []float64{1, 0, 0}, K: 3})
	if err != nil {
		t.Fatalf("knn: %v", err)
	}
	if unfiltered.Count != 3 {
		t.Fatalf("unfiltered count = %d, want 3", unfiltered.Count)
	}
	if unfiltered.Documents[0]["_id"] != "retired-nearest" && unfiltered.Documents[0]["_id"] != "live-near" {
		t.Fatalf("nearest should be one of the parallel vectors, got %v", unfiltered.Documents[0]["_id"])
	}

	filtered, err := e.KNN(context.Background(), "c", contract.KNNRequest{
		Field: "v", Vector: []float64{1, 0, 0}, K: 3,
		Filter: map[string]any{"status": "live"},
	})
	if err != nil {
		t.Fatalf("knn filtered: %v", err)
	}
	if filtered.Count != 2 {
		t.Fatalf("filtered count = %d, want 2 (only live)", filtered.Count)
	}
	for _, d := range filtered.Documents {
		if d["status"] != "live" {
			t.Fatalf("filtered answer carries a non-live document: %v", d["_id"])
		}
	}
}

func TestContextCancelsScans(t *testing.T) {
	e := openEngine(t)
	mustEnsure(t, e, "c")
	docs := make([]map[string]any, 0, 32)
	for i := 0; i < 32; i++ {
		docs = append(docs, map[string]any{"_id": string(rune('a' + i)), "n": float64(i)})
	}
	if _, err := e.Insert(context.Background(), "c", docs); err != nil {
		t.Fatalf("insert: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Query(ctx, "c", contract.Query{Limit: 5}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Query with cancelled ctx: err = %v, want context.Canceled", err)
	}
	if _, err := e.KNN(ctx, "c", contract.KNNRequest{Field: "nope", K: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("KNN with cancelled ctx: err = %v, want context.Canceled", err)
	}
	if _, err := e.KVKeys(ctx, "k", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("KVKeys with cancelled ctx: err = %v, want context.Canceled", err)
	}
	if _, err := e.Insert(ctx, "c", docs); !errors.Is(err, context.Canceled) {
		t.Fatalf("Insert with cancelled ctx: err = %v, want context.Canceled", err)
	}
}

func TestInsertDuplicateIsClassifiable(t *testing.T) {
	e := openEngine(t)
	mustEnsure(t, e, "c")
	if _, err := e.Insert(context.Background(), "c", []map[string]any{{"_id": "a"}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, err := e.Insert(context.Background(), "c", []map[string]any{{"_id": "a"}})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second insert err = %v, want ErrDuplicate", err)
	}
	if IsNotFound(err) {
		t.Fatalf("duplicate must not read as not-found: %v", err)
	}
}

// Declaring an index on a populated collection builds it: the documents
// already stored get their vectors mirrored, so a bulk-load-then-declare
// store is not an index that silently scans nothing.
func TestCreateIndexBackfillsStoredDocuments(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()
	mustEnsure(t, e, "c")
	if _, err := e.Insert(ctx, "c", []map[string]any{
		{"_id": "x", "emb": []float64{1, 0}},
		{"_id": "y", "emb": []float64{0, 1}},
		{"_id": "novet"}, // no vector field: nothing to mirror
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if _, err := e.KNN(ctx, "c", contract.KNNRequest{Field: "emb", Vector: []float64{1, 0}, K: 3}); err == nil {
		t.Fatal("knn before the index exists must fail")
	}
	if err := e.CreateIndexRequest(ctx, "c", contract.IndexRequest{
		Type: "vector", Field: "emb", Dims: 2, Metric: "cosine",
	}); err != nil {
		t.Fatalf("index: %v", err)
	}
	res, err := e.KNN(ctx, "c", contract.KNNRequest{Field: "emb", Vector: []float64{1, 0}, K: 3})
	if err != nil {
		t.Fatalf("knn: %v", err)
	}
	if res.Count != 2 || res.Documents[0]["_id"] != "x" {
		t.Fatalf("after backfill: count=%d first=%v", res.Count, res.Documents[0]["_id"])
	}

	// A dims mismatch in the stored documents fails the build rather than
	// skipping the document.
	mustEnsure(t, e, "d")
	if _, err := e.Insert(ctx, "d", []map[string]any{{"_id": "bad", "emb": []float64{1, 0, 0}}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := e.CreateIndexRequest(ctx, "d", contract.IndexRequest{
		Type: "vector", Field: "emb", Dims: 2,
	}); !errors.Is(err, errDimsMismat) {
		t.Fatalf("backfill dims mismatch err = %v, want errDimsMismat", err)
	}
}
