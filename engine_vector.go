package cumulite

import (
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/willove/cumudb/pkg/client"
	"github.com/dgraph-io/badger/v4"
)

// vectorIndex is a declared vector index: the field it scans, the dims it
// enforces, the metric it answers in and the model behind the vectors.
type vectorIndex struct {
	Name   string `json:"name"`
	Field  string `json:"field"`
	Dims   int    `json:"dims"`
	Metric string `json:"metric"`
	Model  string `json:"model"`
}

// CreateIndexRequest declares a vector index. The other index families the
// full engine offers (persistent, text, graph) are refused by name — the lite
// engine has no query planner to feed them, and pretending otherwise would
// trade a loud failure for a silently wrong plan. Re-declaring an identical
// index succeeds, which keeps ensure-style startup idempotent.
func (e *Engine) CreateIndexRequest(_ context.Context, coll string, request client.IndexRequest) error {
	if request.Type != "vector" {
		return fmt.Errorf("cumulite: unsupported index type %q (vector only)", request.Type)
	}
	if request.Field == "" {
		return errors.New("cumulite: vector index needs a field")
	}
	name := request.Name
	if name == "" {
		name = request.Field
	}
	metric := request.Metric
	if metric == "" {
		metric = "cosine"
	}
	def := vectorIndex{
		Name:   name,
		Field:  request.Field,
		Dims:   request.Dims,
		Metric: metric,
		Model:  request.Model,
	}
	key, err := indexKey(coll, name)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("cumulite: encode index %s/%s: %w", coll, name, err)
	}
	return e.db.Update(func(txn *badger.Txn) error {
		if item, err := txn.Get(key); err == nil {
			existing, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			var have vectorIndex
			if err := json.Unmarshal(existing, &have); err != nil {
				return fmt.Errorf("cumulite: decode index %s/%s: %w", coll, name, err)
			}
			if have != def {
				return fmt.Errorf("cumulite: index %s/%s exists with a different definition (%+v vs %+v)",
					coll, name, have, def)
			}
			return nil // idempotent re-declare
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return fmt.Errorf("cumulite: probe index %s/%s: %w", coll, name, err)
		}
		return txn.Set(key, raw)
	})
}

// listIndexesTxn reads a collection's vector index definitions.
func (e *Engine) listIndexesTxn(txn *badger.Txn, coll string) ([]vectorIndex, error) {
	prefix, err := indexPrefix(coll)
	if err != nil {
		return nil, err
	}
	var out []vectorIndex
	opts := badger.DefaultIteratorOptions
	opts.Prefix = prefix
	it := txn.NewIterator(opts)
	defer it.Close()
	for it.Rewind(); it.Valid(); it.Next() {
		item := it.Item()
		if item.IsDeletedOrExpired() {
			continue
		}
		raw, err := item.ValueCopy(nil)
		if err != nil {
			return nil, fmt.Errorf("cumulite: read index %s: %w", coll, err)
		}
		var def vectorIndex
		if err := json.Unmarshal(raw, &def); err != nil {
			return nil, fmt.Errorf("cumulite: decode index %s: %w", coll, err)
		}
		out = append(out, def)
	}
	return out, nil
}

// KNN answers with the k nearest documents. The engine scans exactly: every
// vector in the index is scored, which at ask's scale (tens of thousands of
// 384-dim vectors) is milliseconds, and it keeps recall exact — no ANN
// structure can drop a neighbour the ranking later needs.
func (e *Engine) KNN(_ context.Context, coll string, request client.KNNRequest) (*client.KNNResult, error) {
	metric := request.Metric
	var def vectorIndex
	err := e.db.View(func(txn *badger.Txn) error {
		indexes, err := e.listIndexesTxn(txn, coll)
		if err != nil {
			return err
		}
		for _, idx := range indexes {
			if request.Index != "" && idx.Name == request.Index {
				def = idx
				break
			}
			if request.Index == "" && idx.Field == request.Field {
				def = idx
				break
			}
		}
		if def.Name == "" {
			return fmt.Errorf("%w: %s/%s", errNoVector, coll, request.Field)
		}
		if metric == "" {
			metric = def.Metric
		}
		if metric == "" {
			metric = "cosine"
		}
		if def.Metric != "" && def.Metric != metric {
			return fmt.Errorf("cumulite: index %s/%s answers %s, request asks %s",
				coll, def.Name, def.Metric, metric)
		}
		if def.Dims > 0 && len(request.Vector) != def.Dims {
			return fmt.Errorf("%w: field %s has %d, index %s declares %d",
				errDimsMismat, request.Field, len(request.Vector), def.Name, def.Dims)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	prefix, err := vecPrefix(coll, def.Field)
	if err != nil {
		return nil, err
	}
	k := request.K
	if k <= 0 {
		return nil, fmt.Errorf("%w: k must be positive", errNoVector)
	}

	var (
		candidates []knnHit
		examined   int
	)
	err = e.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		best := &maxHeap{}
		heap.Init(best)
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			if item.IsDeletedOrExpired() {
				continue
			}
			examined++
			raw, err := item.ValueCopy(nil)
			if err != nil {
				return fmt.Errorf("cumulite: read vector in %s: %w", coll, err)
			}
			id := string(item.Key()[len(prefix):])
			dist, err := distance(metric, request.Vector, decodeVec32(raw))
			if err != nil {
				return fmt.Errorf("cumulite: score %s/%s: %w", coll, id, err)
			}
			heap.Push(best, knnHit{id: id, dist: dist})
			if best.Len() > k {
				heap.Pop(best)
			}
		}
		candidates = append(candidates, best.sorted()...)
		return nil
	})
	if err != nil {
		return nil, err
	}

	docs := make([]map[string]any, 0, len(candidates))
	distances := make([]float64, 0, len(candidates))
	err = e.db.View(func(txn *badger.Txn) error {
		for _, hit := range candidates {
			doc, err := e.getStored(txn, coll, hit.id)
			if err != nil {
				if IsNotFound(err) {
					continue // vector key outlived its document: nothing to rank
				}
				return err
			}
			docs = append(docs, doc)
			distances = append(distances, hit.dist)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &client.KNNResult{
		Documents: docs,
		Distances: distances,
		Count:     len(docs),
		Field:     def.Field,
		Metric:    metric,
		Plan:      "cumulite-exact-scan",
		Examined:  examined,
		Matched:   len(docs),
	}, nil
}

// distance is the server's metric, transcribed so scores agree between the
// two engines: cosine is 1 - cos over clamped similarity (a zero-norm operand
// has no angle and reads as distance 1), l2 is Euclidean, product is negated
// so smaller stays closer.
func distance(metric string, a, b []float64) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("%w: %d vs %d", errDimsMismat, len(a), len(b))
	}
	switch metric {
	case "l2":
		var sum float64
		for i := range a {
			d := a[i] - b[i]
			sum += d * d
		}
		return math.Sqrt(sum), nil
	case "ip":
		var dot float64
		for i := range a {
			dot += a[i] * b[i]
		}
		return -dot, nil
	case "cosine", "":
		var dot, na, nb float64
		for i := range a {
			dot += a[i] * b[i]
			na += a[i] * a[i]
			nb += b[i] * b[i]
		}
		if na == 0 || nb == 0 {
			return 1, nil
		}
		sim := dot / (math.Sqrt(na) * math.Sqrt(nb))
		return 1 - math.Max(-1, math.Min(1, sim)), nil
	}
	return 0, fmt.Errorf("cumulite: unknown metric %q", metric)
}

// knnHit is one scored vector; the heap keeps the k best by discarding the
// current worst, so memory is bounded by k however long the scan is.
type knnHit struct {
	id   string
	dist float64
}

type maxHeap []knnHit

func (h maxHeap) Len() int           { return len(h) }
func (h maxHeap) Less(i, j int) bool { return h[i].dist > h[j].dist }
func (h maxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxHeap) Push(x any)        { *h = append(*h, x.(knnHit)) }
func (h *maxHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// sorted returns the hits closest-first.
func (h maxHeap) sorted() []knnHit {
	out := append([]knnHit(nil), h...)
	sort.Slice(out, func(i, j int) bool { return out[i].dist < out[j].dist })
	return out
}
