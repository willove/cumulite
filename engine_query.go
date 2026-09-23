package cumulite

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/willove/cumudb/pkg/client"
	"github.com/dgraph-io/badger/v4"
)

// Query pages a collection in stable key order. Filters are equality AND over
// the map's fields plus the $or, $in and comparison operators; Skip and Limit
// cut the page. The order is what makes paging sound — an unsorted page whose
// order shifts between reads repeats and drops documents, so the engine always
// walks the keyspace rather than a match order.
func (e *Engine) Query(_ context.Context, coll string, query client.Query) (*client.QueryResult, error) {
	prefix, err := docPrefix(coll)
	if err != nil {
		return nil, err
	}
	limit := query.Limit
	var (
		docs     []map[string]any
		examined int
		matched  int
	)
	err = e.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		skipped := 0
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			if item.IsDeletedOrExpired() {
				continue
			}
			examined++
			raw, err := item.ValueCopy(nil)
			if err != nil {
				return fmt.Errorf("cumulite: read %s: %w", coll, err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				return fmt.Errorf("cumulite: decode in %s: %w", coll, err)
			}
			if query.Filter != nil && !matchFilter(doc, query.Filter) {
				continue
			}
			matched++
			if skipped < query.Skip {
				skipped++
				continue
			}
			if limit <= 0 || len(docs) < limit {
				docs = append(docs, doc)
				continue
			}
			return nil // page complete
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if docs == nil {
		docs = []map[string]any{}
	}
	return &client.QueryResult{
		Documents: docs,
		Count:     len(docs),
		Plan:      "cumulite-key-order-scan",
		Examined:  examined,
		Matched:   matched,
		Sorted:    false,
		Skip:      query.Skip,
		Limit:     query.Limit,
	}, nil
}

// matchFilter evaluates a filter map as AND over its fields, with $or and $and
// grouping clauses. An empty filter matches everything.
func matchFilter(doc map[string]any, filter any) bool {
	fields, ok := filter.(map[string]any)
	if !ok {
		return false
	}
	for key, cond := range fields {
		switch key {
		case "$or":
			if !anyClauseMatches(doc, cond) {
				return false
			}
		case "$and":
			if !allClausesMatch(doc, cond) {
				return false
			}
		default:
			if !matchCond(doc[key], cond) {
				return false
			}
		}
	}
	return true
}

// anyClauseMatches / allClausesMatch walk a clause list whatever slice
// spelling the caller wrote ([]any, []map[string]any), because a filter built
// in Go keeps its literal types and a matcher that only knows []any would
// silently drop the clause.
func anyClauseMatches(doc map[string]any, cond any) bool {
	clauses, ok := sliceElems(cond)
	if !ok || len(clauses) == 0 {
		return false
	}
	for _, clause := range clauses {
		if matchFilter(doc, clause) {
			return true
		}
	}
	return false
}

func allClausesMatch(doc map[string]any, cond any) bool {
	clauses, ok := sliceElems(cond)
	if !ok || len(clauses) == 0 {
		return false
	}
	for _, clause := range clauses {
		if !matchFilter(doc, clause) {
			return false
		}
	}
	return true
}

// sliceElems reads a JSON-ish or Go-literal slice as []any.
func sliceElems(v any) ([]any, bool) {
	switch typed := v.(type) {
	case []any:
		return typed, true
	case []map[string]any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out, true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}

// matchCond evaluates one field condition: an operator map, or plain equality
// (an array field matches when any element equals).
func matchCond(field any, cond any) bool {
	if ops, ok := cond.(map[string]any); ok && len(ops) > 0 {
		for op, operand := range ops {
			switch op {
			case "$eq":
				if !valueEqual(field, operand) {
					return false
				}
			case "$ne":
				if valueEqual(field, operand) {
					return false
				}
			case "$in":
				if !valueIn(field, operand) {
					return false
				}
			case "$gte":
				if c, ok := compareValues(field, operand); !ok || c < 0 {
					return false
				}
			case "$gt":
				if c, ok := compareValues(field, operand); !ok || c <= 0 {
					return false
				}
			case "$lte":
				if c, ok := compareValues(field, operand); !ok || c > 0 {
					return false
				}
			case "$lt":
				if c, ok := compareValues(field, operand); !ok || c >= 0 {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	return valueEqual(field, cond)
}

// valueEqual compares scalars across the spellings JSON and Go literals give
// the same value (float64/int/string/bool), and treats an array field as
// matching when any element equals — a doc carrying topic_keys is found by a
// topic_key query.
func valueEqual(field, want any) bool {
	if arr, ok := field.([]any); ok {
		for _, item := range arr {
			if valueEqual(item, want) {
				return true
			}
		}
		return false
	}
	if a, ok := toFloat(field); ok {
		if b, ok := toFloat(want); ok {
			return a == b
		}
		return false
	}
	return field == want
}

func valueIn(field, list any) bool {
	elems, ok := sliceElems(list)
	if !ok {
		return false
	}
	if f, ok := field.([]any); ok {
		for _, item := range f {
			for _, want := range elems {
				if valueEqual(item, want) {
					return true
				}
			}
		}
		return false
	}
	for _, want := range elems {
		if valueEqual(field, want) {
			return true
		}
	}
	return false
}

// compareValues orders numbers (across int/float spellings) and strings; mixed
// kinds do not compare.
func compareValues(a, b any) (int, bool) {
	if x, ok := toFloat(a); ok {
		if y, ok := toFloat(b); ok {
			switch {
			case x < y:
				return -1, true
			case x > y:
				return 1, true
			default:
				return 0, true
			}
		}
		return 0, false
	}
	if x, ok := a.(string); ok {
		if y, ok := b.(string); ok {
			return strings.Compare(x, y), true
		}
	}
	return 0, false
}

// toFloat reads the numeric spellings a JSON body or a Go literal can hold.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
