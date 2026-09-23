package cumulite

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/willove/cumudb/pkg/client"
)

func openEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := Open("", WithInMemory())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func mustEnsure(t *testing.T, e *Engine, coll string) {
	t.Helper()
	if err := e.EnsureCollection(context.Background(), coll); err != nil {
		t.Fatalf("ensure: %v", err)
	}
}

func mustInsert(t *testing.T, e *Engine, coll string, docs ...map[string]any) []string {
	t.Helper()
	ids, err := e.Insert(context.Background(), coll, docs)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return ids
}

func TestInsertGetDelete(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()

	// Writes fail closed on an undeclared collection, the way the server does:
	// a typo'd identity must not silently become a new empty collection.
	if _, err := e.Insert(ctx, "c", []map[string]any{{"_id": "a", "n": 1.0}}); err == nil {
		t.Fatal("insert into undeclared collection must fail")
	} else if !IsNotFound(err) {
		t.Fatalf("undeclared collection err = %v, want not-found", err)
	}
	if err := e.EnsureCollection(ctx, "c"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := e.EnsureCollection(ctx, "c"); err != nil {
		t.Fatalf("ensure again: %v", err)
	}

	ids := mustInsert(t, e, "c", map[string]any{"_id": "a", "n": 1.0})
	if len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("ids = %v", ids)
	}
	doc, err := e.GetDocument(ctx, "c", "a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if doc["_id"] != "a" || doc["n"] != 1.0 {
		t.Fatalf("doc = %v", doc)
	}

	if _, err := e.Insert(ctx, "c", []map[string]any{{"_id": "a"}}); err == nil {
		t.Fatal("duplicate insert must fail")
	}

	// a document without _id still lands under a generated identity
	gen := mustInsert(t, e, "c", map[string]any{"n": 2.0})
	if len(gen) != 1 || gen[0] == "" {
		t.Fatalf("generated id = %q", gen[0])
	}
	if _, err := e.GetDocument(ctx, "c", gen[0]); err != nil {
		t.Fatalf("get generated: %v", err)
	}

	if _, err := e.GetDocument(ctx, "c", "missing"); !IsNotFound(err) {
		t.Fatalf("get missing err = %v, want not-found", err)
	}

	existed, err := e.DeleteDocument(ctx, "c", "a")
	if err != nil || !existed {
		t.Fatalf("delete = %v, %v", existed, err)
	}
	existed, err = e.DeleteDocument(ctx, "c", "a")
	if err != nil || existed {
		t.Fatalf("second delete = %v, %v", existed, err)
	}
}

func TestReplaceAndPatch(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()

	mustEnsure(t, e, "c")
	mustInsert(t, e, "c", map[string]any{"_id": "a", "status": "active", "body": "x"})

	if _, err := e.ReplaceDocument(ctx, "c", "missing", map[string]any{"_id": "missing"}); !IsNotFound(err) {
		t.Fatalf("replace missing err = %v, want not-found", err)
	}
	stored, err := e.ReplaceDocument(ctx, "c", "a", map[string]any{"status": "stale"})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if stored["status"] != "stale" || stored["body"] != nil {
		t.Fatalf("replaced doc = %v (replace is full state)", stored)
	}

	if _, err := e.PatchDocument(ctx, "c", "missing", map[string]any{"$set": map[string]any{"x": 1}}); !IsNotFound(err) {
		t.Fatalf("patch missing err = %v, want not-found", err)
	}
	if _, err := e.PatchDocument(ctx, "c", "a", map[string]any{"$unset": map[string]any{"x": 1}}); err == nil {
		t.Fatal("unsupported operator must be refused")
	}
	patched, err := e.PatchDocument(ctx, "c", "a", map[string]any{"$set": map[string]any{"status": "deleted"}})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if patched["status"] != "deleted" {
		t.Fatalf("patched doc = %v", patched)
	}
}

func TestQueryFiltersAndPaging(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()

	mustEnsure(t, e, "c")

	var docs []map[string]any
	for i := 0; i < 25; i++ {
		status := "live"
		if i%5 == 0 {
			status = "retired"
		}
		docs = append(docs, map[string]any{
			"_id":    fmt.Sprintf("d%02d", i),
			"status": status,
			"score":  float64(i),
			"tags":   []any{"even", "odd"},
		})
	}
	mustInsert(t, e, "c", docs...)

	page1, err := e.Query(ctx, "c", client.Query{Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page1.Documents) != 10 || page1.Count != 10 {
		t.Fatalf("page1 = %d docs", len(page1.Documents))
	}
	if page1.Documents[0]["_id"] != "d00" {
		t.Fatalf("first = %v (key order)", page1.Documents[0]["_id"])
	}
	page2, err := e.Query(ctx, "c", client.Query{Skip: 10, Limit: 10})
	if err != nil {
		t.Fatalf("query skip: %v", err)
	}
	if page2.Documents[0]["_id"] != "d10" {
		t.Fatalf("page2 first = %v", page2.Documents[0]["_id"])
	}
	all, err := e.Query(ctx, "c", client.Query{})
	if err != nil {
		t.Fatalf("query all: %v", err)
	}
	if len(all.Documents) != 25 || all.Documents[24]["_id"] != "d24" {
		t.Fatalf("all = %d docs, last = %v", len(all.Documents), all.Documents[24]["_id"])
	}

	live, err := e.Query(ctx, "c", client.Query{Filter: map[string]any{"status": "live"}})
	if err != nil {
		t.Fatalf("query live: %v", err)
	}
	if live.Matched != 20 {
		t.Fatalf("live = %d", live.Matched)
	}

	in, err := e.Query(ctx, "c", client.Query{Filter: map[string]any{"_id": map[string]any{"$in": []any{"d01", "d07", "nope"}}}})
	if err != nil {
		t.Fatalf("query in: %v", err)
	}
	if len(in.Documents) != 2 {
		t.Fatalf("in = %d docs", len(in.Documents))
	}

	gte, err := e.Query(ctx, "c", client.Query{Filter: map[string]any{"score": map[string]any{"$gte": 20.0}}})
	if err != nil {
		t.Fatalf("query gte: %v", err)
	}
	if len(gte.Documents) != 5 || gte.Documents[0]["_id"] != "d20" {
		t.Fatalf("gte = %d", len(gte.Documents))
	}

	both, err := e.Query(ctx, "c", client.Query{Filter: map[string]any{
		"status": "live", "score": map[string]any{"$gte": 22.0},
	}})
	if err != nil {
		t.Fatalf("query and: %v", err)
	}
	if len(both.Documents) != 3 {
		t.Fatalf("and = %d", len(both.Documents))
	}

	// array field: an element match is a field match
	tags, err := e.Query(ctx, "c", client.Query{Filter: map[string]any{"tags": "even"}, Limit: 1000})
	if err != nil {
		t.Fatalf("query tags: %v", err)
	}
	if tags.Matched != 25 {
		t.Fatalf("tags = %d", tags.Matched)
	}

	or, err := e.Query(ctx, "c", client.Query{Filter: map[string]any{
		"$or": []any{
			map[string]any{"_id": "d03"},
			map[string]any{"status": "retired"},
		},
	}})
	if err != nil {
		t.Fatalf("query or: %v", err)
	}
	if or.Matched != 6 { // 5 retired + d03
		t.Fatalf("or = %d", or.Matched)
	}

	// Skip beyond the end: an empty page, not an error
	empty, err := e.Query(ctx, "c", client.Query{Skip: 1000, Limit: 10})
	if err != nil {
		t.Fatalf("query skip past end: %v", err)
	}
	if len(empty.Documents) != 0 {
		t.Fatalf("skip past end = %d", len(empty.Documents))
	}

	// Go-literal slice spellings: $or built as []map[string]any and $in with
	// []string must read the same as their JSON-decoded []any forms.
	goOr, err := e.Query(ctx, "c", client.Query{Filter: map[string]any{
		"$or": []map[string]any{
			{"_id": "d03"},
			{"status": "retired"},
		},
	}})
	if err != nil {
		t.Fatalf("query go-or: %v", err)
	}
	if goOr.Matched != or.Matched {
		t.Fatalf("[]map[string]any $or matched %d, []any $or matched %d", goOr.Matched, or.Matched)
	}
	goIn, err := e.Query(ctx, "c", client.Query{Filter: map[string]any{
		"_id": map[string]any{"$in": []string{"d01", "d07", "nope"}},
	}})
	if err != nil {
		t.Fatalf("query go-in: %v", err)
	}
	if len(goIn.Documents) != 2 {
		t.Fatalf("[]string $in = %d docs", len(goIn.Documents))
	}
}

func TestKV(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()

	if _, err := e.KVGet(ctx, "n:1"); !IsNotFound(err) {
		t.Fatalf("get missing err = %v, want not-found", err)
	}
	if err := e.KVPut(ctx, "n:1", []byte("v"), 0); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := e.KVPut(ctx, "n:2", []byte("w"), 0); err != nil {
		t.Fatalf("put 2: %v", err)
	}
	if err := e.KVPut(ctx, "other:1", []byte("x"), 0); err != nil {
		t.Fatalf("put other: %v", err)
	}
	got, err := e.KVGet(ctx, "n:1")
	if err != nil || string(got) != "v" {
		t.Fatalf("get = %q, %v", got, err)
	}
	keys, err := e.KVKeys(ctx, "n:", 0)
	if err != nil || len(keys) != 2 || keys[0] != "n:1" {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	keys, err = e.KVKeys(ctx, "n:", 1)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys limited = %v, %v", keys, err)
	}
	existed, err := e.KVDelete(ctx, "n:1")
	if err != nil || !existed {
		t.Fatalf("delete = %v, %v", existed, err)
	}
	existed, err = e.KVDelete(ctx, "n:1")
	if err != nil || existed {
		t.Fatalf("delete again = %v, %v", existed, err)
	}
}

func TestKVTTLExpires(t *testing.T) {
	if testing.Short() {
		t.Skip("ttl expiry needs a real second")
	}
	e := openEngine(t)
	ctx := context.Background()
	if err := e.KVPut(ctx, "tmp", []byte("v"), time.Second); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := e.KVGet(ctx, "tmp"); err != nil {
		t.Fatalf("get before expiry: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := e.KVGet(ctx, "tmp"); !IsNotFound(err) {
		t.Fatalf("get after expiry err = %v, want not-found", err)
	}
}

func vecOf(f func(i int) float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = f(i)
	}
	return out
}

func TestVectorIndexAndKNN(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()

	mustEnsure(t, e, "c")

	if err := e.CreateIndexRequest(ctx, "c", client.IndexRequest{
		Name: "ask_body_embed", Field: "body_embed", Type: "vector",
		Dims: 4, Metric: "cosine", Model: "minilm",
	}); err != nil {
		t.Fatalf("index: %v", err)
	}
	// idempotent re-declare
	if err := e.CreateIndexRequest(ctx, "c", client.IndexRequest{
		Name: "ask_body_embed", Field: "body_embed", Type: "vector",
		Dims: 4, Metric: "cosine", Model: "minilm",
	}); err != nil {
		t.Fatalf("re-declare: %v", err)
	}
	// conflicting re-declare
	if err := e.CreateIndexRequest(ctx, "c", client.IndexRequest{
		Name: "ask_body_embed", Field: "body_embed", Type: "vector",
		Dims: 8, Metric: "cosine",
	}); err == nil {
		t.Fatal("conflicting re-declare must fail")
	}
	// non-vector family refused, not silently ignored
	if err := e.CreateIndexRequest(ctx, "c", client.IndexRequest{
		Name: "pers", Field: "status", Type: "persistent",
	}); err == nil {
		t.Fatal("persistent index must be refused")
	}

	// wrong dims refused at write time
	if _, err := e.Insert(ctx, "c", []map[string]any{{"_id": "bad", "body_embed": []float64{1, 2}}}); err == nil {
		t.Fatal("wrong-dim vector must fail the write")
	}

	mustInsert(t, e, "c",
		map[string]any{"_id": "near", "body_embed": []float64{1, 0, 0, 0}},
		map[string]any{"_id": "mid", "body_embed": []float64{0.9, 0.1, 0, 0}},
		map[string]any{"_id": "far", "body_embed": []float64{0, 0, 1, 0}},
		map[string]any{"_id": "zero", "body_embed": []float64{0, 0, 0, 0}},
	)

	res, err := e.KNN(ctx, "c", client.KNNRequest{
		Field: "body_embed", Vector: []float64{1, 0, 0, 0}, K: 3, Metric: "cosine",
	})
	if err != nil {
		t.Fatalf("knn: %v", err)
	}
	if len(res.Documents) != 3 {
		t.Fatalf("knn docs = %d", len(res.Documents))
	}
	wantOrder := []string{"near", "mid", "zero"} // zero reads as distance 1, better than far
	for i, want := range wantOrder {
		if res.Documents[i]["_id"] != want {
			t.Fatalf("order[%d] = %v, want %s (all: %v)", i, res.Documents[i]["_id"], want, res.Documents)
		}
	}
	if res.Distances[0] != 0 {
		t.Fatalf("self distance = %v", res.Distances[0])
	}
	if math.Abs(res.Distances[1]-(1-0.9940)) > 0.01 {
		t.Fatalf("mid distance = %v", res.Distances[1])
	}
	if res.Distances[2] != 1 { // zero-norm: no defined angle
		t.Fatalf("zero distance = %v, want 1", res.Distances[2])
	}
	if res.Examined != 4 || res.Matched != 3 || res.Field != "body_embed" || res.Metric != "cosine" {
		t.Fatalf("result meta = %+v", res)
	}

	// k larger than the corpus returns what exists
	res, err = e.KNN(ctx, "c", client.KNNRequest{Field: "body_embed", Vector: []float64{1, 0, 0, 0}, K: 100})
	if err != nil || len(res.Documents) != 4 {
		t.Fatalf("knn k=100: %v docs, %v", len(res.Documents), err)
	}

	// dims mismatch on the query vector
	if _, err := e.KNN(ctx, "c", client.KNNRequest{Field: "body_embed", Vector: []float64{1, 0}, K: 2}); err == nil {
		t.Fatal("query dims mismatch must fail")
	}
	// no index on a field
	if _, err := e.KNN(ctx, "c", client.KNNRequest{Field: "nope", Vector: []float64{1, 0, 0, 0}, K: 2}); err == nil {
		t.Fatal("knn without index must fail")
	}

	// deleting a document drops its vector from the scan
	if _, err := e.DeleteDocument(ctx, "c", "near"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	res, err = e.KNN(ctx, "c", client.KNNRequest{Field: "body_embed", Vector: []float64{1, 0, 0, 0}, K: 3})
	if err != nil {
		t.Fatalf("knn after delete: %v", err)
	}
	if res.Documents[0]["_id"] != "mid" || res.Examined != 3 {
		t.Fatalf("after delete: %v, examined %d", res.Documents[0]["_id"], res.Examined)
	}
}

func TestChangelog(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()

	mustEnsure(t, e, "c")

	// recording is off until asked for
	mustInsert(t, e, "c", map[string]any{"_id": "a"})
	page, err := e.Changes(ctx, "c", 0, 10)
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	if len(page.Changes) != 0 || page.Enabled {
		t.Fatalf("changes before enable = %+v", page)
	}

	if err := e.SetChangelog(ctx, "c", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	mustInsert(t, e, "c", map[string]any{"_id": "b"})
	if _, err := e.PatchDocument(ctx, "c", "b", map[string]any{"$set": map[string]any{"v": 1}}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	page, err = e.Changes(ctx, "c", 0, 10)
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	if len(page.Changes) != 2 || !page.Enabled {
		t.Fatalf("changes = %d, enabled %v", len(page.Changes), page.Enabled)
	}
	if page.Changes[0].ID != "b" || page.Changes[0].Op != "insert" || page.Changes[0].Sequence != 1 {
		t.Fatalf("first change = %+v", page.Changes[0])
	}
	if page.Changes[1].Op != "patch" || page.Changes[1].Sequence != 2 {
		t.Fatalf("second change = %+v", page.Changes[1])
	}
	if page.Cursor != 2 {
		t.Fatalf("cursor = %d", page.Cursor)
	}

	// resuming from the cursor: nothing new
	page, err = e.Changes(ctx, "c", page.Cursor, 10)
	if err != nil {
		t.Fatalf("changes from cursor: %v", err)
	}
	if len(page.Changes) != 0 || page.Cursor != 2 {
		t.Fatalf("resume = %+v", page)
	}

	// a page smaller than the backlog advances the cursor, not skips it
	page, err = e.Changes(ctx, "c", 0, 1)
	if err != nil {
		t.Fatalf("changes limit 1: %v", err)
	}
	if len(page.Changes) != 1 || page.Cursor != 1 {
		t.Fatalf("paged = %+v", page)
	}

	// deletes are recorded too
	if _, err := e.DeleteDocument(ctx, "c", "b"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	page, err = e.Changes(ctx, "c", 0, 10)
	if err != nil {
		t.Fatalf("changes after delete: %v", err)
	}
	if len(page.Changes) != 3 || page.Changes[2].Op != "delete" {
		t.Fatalf("changes after delete = %+v", page.Changes)
	}

	// disabling stops recording, keeps history
	if err := e.SetChangelog(ctx, "c", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	mustInsert(t, e, "c", map[string]any{"_id": "z"})
	page, err = e.Changes(ctx, "c", 0, 10)
	if err != nil {
		t.Fatalf("changes after disable: %v", err)
	}
	if len(page.Changes) != 3 || page.Enabled {
		t.Fatalf("changes after disable = %d, enabled %v", len(page.Changes), page.Enabled)
	}
}

func TestHealth(t *testing.T) {
	e := openEngine(t)
	h, err := e.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if h.Status != "ok" || h.Backend != "badger" {
		t.Fatalf("health = %+v", h)
	}
}

// The engine must keep satisfying the port the HTTP client satisfies — that
// identity is the whole point of the module.
var _ Port = (*Engine)(nil)

func TestNotFoundErrorIdiom(t *testing.T) {
	e := openEngine(t)
	_, err := e.GetDocument(context.Background(), "c", "x")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("errors.Is(ErrNotFound) = false for %v", err)
	}
	if !client.IsNotFound(err) {
		t.Fatalf("client.IsNotFound = false for %v", err)
	}
}
