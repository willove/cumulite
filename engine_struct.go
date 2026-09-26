package cumulite

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// This file is the typed-write half of the anti-drift kit. The incident it
// exists for: a consumer hand-maintained a struct→map translation table, a new
// struct field missed the table, and the map path stored the document without
// it — silently, because reads decode the whole document and only the missing
// field's readers ever notice. The exits are three: write structs directly
// (InsertStructs/ReplaceStruct, so the json tags are the single source of
// truth), declare a collection shape and let every write be audited against it
// (engine_shape.go), or prove the round-trip after writing (VerifyDoc below).

// structToDoc turns any Go value into the map[string]any the document write
// path stores, by exactly the round-trip putStored itself applies: json.Marshal
// honours the struct's tags (names, omitempty, inlining), the unmarshal lands
// numbers as float64 the same way a readback decodes them. A struct and the
// map an honest hand-translation would build therefore reach storage
// byte-identically — the only difference is the struct cannot forget a field.
func structToDoc(v any) (map[string]any, error) {
	if v == nil {
		return nil, fmt.Errorf("cumulite: nil document")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("cumulite: encode document: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("cumulite: decode document: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("cumulite: nil document")
	}
	return doc, nil
}

// InsertStructs stores whole Go structs as documents: the structs' json tags
// are the field names, and the documents go through the same path as Insert
// (identity from _id/_key or generated, duplicate ids fail the batch). A typed
// consumer should prefer this over hand-building map[string]any — the struct
// definition then is the single source of truth, and a new field can no longer
// be dropped by a translation table that forgot to grow it.
//
// The storage format is unchanged from the map path: keys are the tags, times
// are RFC3339Nano, a nil pointer field with omitempty is absent exactly like
// the conditional write a hand-table would spell. Map values are accepted too
// (they normalize through the same round-trip), which makes the entry a
// drop-in wherever Insert already fits.
func (e *Engine) InsertStructs(ctx context.Context, coll string, docs []any) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	documents := make([]map[string]any, len(docs))
	for i, doc := range docs {
		converted, err := structToDoc(doc)
		if err != nil {
			return nil, err
		}
		documents[i] = converted
	}
	return e.Insert(ctx, coll, documents)
}

// ReplaceStruct replaces one document from a struct value — the typed spelling
// of ReplaceDocument, with structToDoc's guarantees: tags name the fields and
// no translation table stands between the struct and storage.
func (e *Engine) ReplaceStruct(ctx context.Context, coll, id string, doc any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	converted, err := structToDoc(doc)
	if err != nil {
		return nil, err
	}
	return e.ReplaceDocument(ctx, coll, id, converted)
}

// VerifyDoc re-marshals original, reads back what storage actually holds under
// coll/id, and returns the diff: "lost: k" for keys the write dropped, "gained:
// k" for keys storage holds that original does not, "changed: k" for keys whose
// value came back different. An empty diff is the proof the round-trip is
// honest.
//
// It is the one-call reproduction of a shape-audit finding, usable without any
// declared shape because it compares against original itself, not a type:
// write whatever the write path wrote, then ask what landed. Intended for
// tests, ops checks, and any write path that claims to be durable — the check
// a hand-built translation table never gets for free.
//
// Identity bookkeeping is not drift: a stored _id (or _key) that merely echoes
// the id argument and is absent from original does not count as gained.
func (e *Engine) VerifyDoc(ctx context.Context, coll, id string, original any) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	want, err := structToDoc(original)
	if err != nil {
		return nil, err
	}
	stored, err := e.GetDocument(ctx, coll, id)
	if err != nil {
		return nil, err
	}
	var diff []string
	for _, k := range sortedKeys(want) {
		got, ok := stored[k]
		if !ok {
			diff = append(diff, "lost: "+k)
			continue
		}
		if !jsonEqual(want[k], got) {
			diff = append(diff, fmt.Sprintf("changed: %s (want %s got %s)", k, jsonish(want[k]), jsonish(got)))
		}
	}
	for _, k := range sortedKeys(stored) {
		if _, ok := want[k]; ok {
			continue
		}
		if (k == "_id" || k == "_key") && stored[k] == id {
			continue // the identity the engine stamped, not extra data
		}
		diff = append(diff, "gained: "+k)
	}
	return diff, nil
}

// jsonEqual compares two JSON-decoded values. Both sides of the comparison
// reached this shape through decode (the stored side by reading, the wanted
// side by structToDoc's round-trip), so reflect.DeepEqual on the trees is the
// same comparison byte equality is, minus formatting luck.
func jsonEqual(a, b any) bool {
	rawA, errA := json.Marshal(a)
	rawB, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(rawA) == string(rawB)
}

// jsonish renders a JSON-decoded value for a diff line.
func jsonish(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	if len(raw) > 200 {
		return string(raw[:200]) + "…"
	}
	return string(raw)
}

// sortedKeys returns a map's keys in sorted order so a diff reads the same
// twice.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
