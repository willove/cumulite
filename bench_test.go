package cumulite

import (
	"context"
	"fmt"
	"testing"

	"github.com/willove/cumulite/contract"
)

// benchCorpus builds n documents shaped like the suite's articles: a title, a
// couple-hundred-rune body, status/meta scalars the query path filters on.
func benchCorpus(n int) []map[string]any {
	body := "《测试法》第"
	docs := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		docs = append(docs, map[string]any{
			"_id":          fmt.Sprintf("L%03d-A%05d", i%176, i),
			"title":        fmt.Sprintf("测试法·第%d条", i),
			"body":         fmt.Sprintf("%s%d条规定，当事人应当按照约定全面履行自己的义务，遵循诚信原则。", body, i),
			"source_type":  "txt",
			"status":       "active",
			"business_key": fmt.Sprintf("L%03d-A%05d", i%176, i),
			"lang":         "zh",
			"version":      1,
			"digest":       fmt.Sprintf("%064d", i),
		})
	}
	return docs
}

// BenchmarkAskScale inserts and pages the suite's real corpus size (14313
// articles), the workload ActiveSources performs on every search: full-collection
// paging plus the filtered variants (evidence window, id batch).
func BenchmarkAskScale(b *testing.B) {
	ctx := context.Background()
	const n = 14313
	docs := benchCorpus(n)

	b.Run("insert", func(b *testing.B) {
		e, err := Open("", WithInMemory())
		if err != nil {
			b.Fatal(err)
		}
		defer e.Close()
		if err := e.EnsureCollection(ctx, "ask_sources"); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for start := 0; start < n; start += 500 {
				if _, err := e.Insert(ctx, "ask_sources", docs[start:min(start+500, n)]); err != nil {
					b.Fatal(err)
				}
			}
		}
	})

	e, err := Open("", WithInMemory())
	if err != nil {
		b.Fatal(err)
	}
	defer e.Close()
	if err := e.EnsureCollection(ctx, "ask_sources"); err != nil {
		b.Fatal(err)
	}
	for start := 0; start < n; start += 500 {
		if _, err := e.Insert(ctx, "ask_sources", docs[start:min(start+500, n)]); err != nil {
			b.Fatal(err)
		}
	}

	b.Run("active-sources-full-sweep", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			seen := 0
			for skip := 0; ; skip += 1000 {
				res, err := e.Query(ctx, "ask_sources", contract.Query{Limit: 1000, Skip: skip})
				if err != nil {
					b.Fatal(err)
				}
				seen += len(res.Documents)
				if len(res.Documents) < 1000 {
					break
				}
			}
			if seen != n {
				b.Fatalf("swept %d, want %d", seen, n)
			}
		}
	})

	b.Run("evidence-window-filter", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := e.Query(ctx, "ask_sources", contract.Query{
				Filter: map[string]any{"status": "active", "version": map[string]any{"$gte": 1.0}},
				Limit:  200,
			}); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ids-batch", func(b *testing.B) {
		ids := make([]any, 0, 500)
		for i := 0; i < 500; i++ {
			ids = append(ids, docs[i*7]["_id"])
		}
		for i := 0; i < b.N; i++ {
			res, err := e.Query(ctx, "ask_sources", contract.Query{
				Filter: map[string]any{"_id": map[string]any{"$in": ids}}, Limit: 500,
			})
			if err != nil {
				b.Fatal(err)
			}
			if len(res.Documents) != 500 {
				b.Fatalf("batch returned %d, want 500", len(res.Documents))
			}
		}
	})
}

// BenchmarkKNN384 scores the vector path at the suite's corpus size with
// minilm-384 vectors — the L1 cache query the -l1pre flag runs.
func BenchmarkKNN384(b *testing.B) {
	ctx := context.Background()
	const n = 14313
	e, err := Open("", WithInMemory())
	if err != nil {
		b.Fatal(err)
	}
	defer e.Close()
	if err := e.EnsureCollection(ctx, "ask_sources"); err != nil {
		b.Fatal(err)
	}
	if err := e.CreateIndexRequest(ctx, "ask_sources", contract.IndexRequest{
		Name: "ask_body_embed", Field: "body_embed", Type: "vector",
		Dims: 384, Metric: "cosine", Model: "minilm",
	}); err != nil {
		b.Fatal(err)
	}
	docs := benchCorpus(n)
	for i, doc := range docs {
		vec := make([]float64, 384)
		for j := range vec {
			vec[j] = float64((i*31+j*7)%97) / 97.0
		}
		doc["body_embed"] = vec
	}
	for start := 0; start < n; start += 500 {
		if _, err := e.Insert(ctx, "ask_sources", docs[start:min(start+500, n)]); err != nil {
			b.Fatal(err)
		}
	}
	query := make([]float64, 384)
	for j := range query {
		query[j] = float64((3*31+j*7)%97) / 97.0
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := e.KNN(ctx, "ask_sources", contract.KNNRequest{
			Field: "body_embed", Vector: query, K: 8, Metric: "cosine",
		})
		if err != nil {
			b.Fatal(err)
		}
		if len(res.Documents) != 8 {
			b.Fatalf("knn returned %d, want 8", len(res.Documents))
		}
	}
}
