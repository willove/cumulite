package contract

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// The fork of cumudb's wire contract drifted silently unless something pinned
// the field set. These golden JSON dumps are that pin: renaming, retyping or
// dropping a field changes the bytes, and the test says which type moved.

func TestQueryGolden(t *testing.T) {
	raw, err := json.Marshal(&Query{Filter: "f", Sort: "s", Projection: "p", Skip: 1, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Filter/Sort/Projection are `any` with no tags — they serialize under their
	// Go field names, exactly as the HTTP engine does.
	if got, want := string(raw), `{"Filter":"f","Sort":"s","Projection":"p","Skip":1,"Limit":2}`; got != want {
		t.Fatalf("Query json = %s, want %s", got, want)
	}
}

func TestQueryResultGolden(t *testing.T) {
	raw, err := json.Marshal(&QueryResult{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"documents":null,"count":0,"plan":"","examined":0,"matched":0,"sorted":false,"skip":0,"limit":0}`
	if got := string(raw); got != want {
		t.Fatalf("QueryResult json = %s, want %s", got, want)
	}
}

func TestKNNGolden(t *testing.T) {
	raw, err := json.Marshal(&KNNRequest{Field: "f", Vector: []float64{1.5}, K: 8, Metric: "cosine", Filter: map[string]any{"a": 1}, Index: "i"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"field":"f","vector":[1.5],"k":8,"metric":"cosine","filter":{"a":1},"index":"i"}`
	if got := string(raw); got != want {
		t.Fatalf("KNNRequest json = %s, want %s", got, want)
	}

	raw, err = json.Marshal(&KNNResult{Count: 1, Field: "f", Metric: "cosine", Plan: "scan", Examined: 2, Matched: 1})
	if err != nil {
		t.Fatal(err)
	}
	want = `{"documents":null,"distances":null,"count":1,"field":"f","metric":"cosine","plan":"scan","examined":2,"matched":1}`
	if got := string(raw); got != want {
		t.Fatalf("KNNResult json = %s, want %s", got, want)
	}
}

func TestChangeGolden(t *testing.T) {
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(&ChangeRecord{Sequence: 7, Op: "insert", ID: "a", Created: true, At: at})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"sequence":7,"op":"insert","id":"a","created":true,"at":"2026-09-23T12:00:00Z"}`
	if got := string(raw); got != want {
		t.Fatalf("ChangeRecord json = %s, want %s", got, want)
	}

	raw, err = json.Marshal(&ChangesPage{Count: 1, Cursor: 7, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	want = `{"changes":null,"count":1,"cursor":7,"enabled":true}`
	if got := string(raw); got != want {
		t.Fatalf("ChangesPage json = %s, want %s", got, want)
	}
}

func TestIndexRequestGolden(t *testing.T) {
	raw, err := json.Marshal(&IndexRequest{Name: "n", Field: "f", Type: "vector", Dims: 384, Metric: "cosine", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"n","field":"f","type":"vector","dims":384,"metric":"cosine","model":"m"}`
	if got := string(raw); got != want {
		t.Fatalf("IndexRequest json = %s, want %s", got, want)
	}
}

func TestHealthGolden(t *testing.T) {
	raw, err := json.Marshal(&Health{Status: "ok", Version: "cumulite/0.1.0", Backend: "badger", InMemory: true, UptimeSec: 3})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"status":"ok","version":"cumulite/0.1.0","backend":"badger","inMemory":true,"uptimeSec":3}`
	if got := string(raw); got != want {
		t.Fatalf("Health json = %s, want %s", got, want)
	}
}

func TestIsNotFound(t *testing.T) {
	if IsNotFound(nil) {
		t.Fatal("nil must not be not-found")
	}
	wrapped := fmt.Errorf("cumulite: document c/a: %w", ErrNotFound)
	if !IsNotFound(wrapped) {
		t.Fatalf("wrapped sentinel not recognized: %v", wrapped)
	}
	api := &APIError{Status: 404, Code: "NOT_FOUND"}
	if !IsNotFound(api) {
		t.Fatal("404 APIError must be not-found")
	}
	if IsNotFound(&APIError{Status: 500}) {
		t.Fatal("500 APIError must not be not-found")
	}
}
