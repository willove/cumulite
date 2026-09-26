package cumulite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

// This file is the shape half of the anti-drift kit: declare a collection's
// canonical document shape once (SetCollectionShape), and every document write
// through the engine is audited against it — keys the shape requires but the
// document lacks (Missing: the translation-table-forgot-a-field drift) and
// keys no shape tag claims (Unknown: the renamed or typo'd key). Strict mode
// fails such writes; lenient mode stores them but remembers the finding for
// LastShapeAudit, and ShapeReport is the query-side ruler a parity test or an
// ops check runs against any document. A collection with no declared shape
// behaves exactly as it did before — declaring is opt-in per collection.

// shapeField is one json tag the shape remembers: the document key and whether
// the field may be lawfully absent (omitempty).
type shapeField struct {
	Name      string `json:"name"`
	Omitempty bool   `json:"omitempty,omitempty"`
}

// shapeRecord is what storage keeps per shaped collection (no documents are
// rewritten when a shape is declared or dropped — the record only arms the
// audit of future writes).
type shapeRecord struct {
	Fields []shapeField `json:"fields"`
	Strict bool         `json:"strict,omitempty"`
}

// ShapeAudit is the finding of one document-vs-shape comparison.
//
// Missing and Unknown are the drift that matters — required tags absent from
// the document, document keys no tag claims. Absent is informational: omitempty
// tags that happen to be zero in this document, legal at write time but worth
// seeing when a test asserts what a specific write should have carried.
type ShapeAudit struct {
	Collection string   `json:"collection"`
	DocumentID string   `json:"documentId,omitempty"`
	Missing    []string `json:"missing,omitempty"`
	Unknown    []string `json:"unknown,omitempty"`
	Absent     []string `json:"absent,omitempty"`
	Strict     bool     `json:"strict,omitempty"`
}

// violated reports whether the audit found drift strict mode fails on.
func (a ShapeAudit) violated() bool {
	return len(a.Missing) > 0 || len(a.Unknown) > 0
}

// empty reports whether the audit found nothing at all, Absent included.
func (a ShapeAudit) empty() bool {
	return len(a.Missing) == 0 && len(a.Unknown) == 0 && len(a.Absent) == 0
}

func (a ShapeAudit) String() string {
	parts := make([]string, 0, 3)
	if len(a.Missing) > 0 {
		parts = append(parts, "missing "+fmt.Sprintf("%v", a.Missing))
	}
	if len(a.Unknown) > 0 {
		parts = append(parts, "unknown "+fmt.Sprintf("%v", a.Unknown))
	}
	if len(a.Absent) > 0 {
		parts = append(parts, "absent(optional) "+fmt.Sprintf("%v", a.Absent))
	}
	return strings.Join(parts, ", ")
}

// shapeFieldsOf reflects the json tags of a struct value's type: the keys the
// documents of a shaped collection carry. It follows encoding/json's visible
// rules — exported fields, `json:"name"` naming, omitempty, `-` excluded, and
// anonymous struct (or pointer-to-struct) fields inlined at this level — which
// is exactly the set structToDoc's round-trip produces.
func shapeFieldsOf(t reflect.Type, depth int) ([]shapeField, error) {
	if depth > 32 {
		return nil, errors.New("cumulite: shape struct embeds too deep (cycle?)")
	}
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil, errors.New("cumulite: shape must be a struct value (pass a zero value of the domain struct)")
	}
	var out []shapeField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue // unexported
		}
		parts := strings.Split(f.Tag.Get("json"), ",")
		if parts[0] == "-" {
			continue
		}
		omitempty := false
		for _, opt := range parts[1:] {
			if opt == "omitempty" {
				omitempty = true
			}
		}
		// An anonymous struct field with no json name is inlined by
		// encoding/json: its tags belong at this level of the shape.
		if f.Anonymous && parts[0] == "" {
			ft := f.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				inlined, err := shapeFieldsOf(ft, depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, inlined...)
				continue
			}
		}
		name := parts[0]
		if name == "" {
			name = f.Name
		}
		out = append(out, shapeField{Name: name, Omitempty: omitempty})
	}
	return out, nil
}

// SetCollectionShape declares a collection's canonical document shape: pass a
// zero value of the domain struct, and the engine reflects its json tags once
// and remembers them per collection (persisted, so it survives restarts like
// every other per-collection setting). From then on every document write to
// the collection — Insert, Replace, Struct variants, Patch's result — is
// audited against the tags, and a shape violation is either a failed write
// (strict) or a remembered finding (lenient; see LastShapeAudit).
//
// Re-declaring replaces the field set and keeps the strict flag, so an
// ensure-style startup can declare every launch. Collections without a
// declared shape are not audited at all.
func (e *Engine) SetCollectionShape(ctx context.Context, coll string, shape any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fields, err := shapeFieldsOf(reflect.TypeOf(shape), 0)
	if err != nil {
		return err
	}
	key, err := shapeKey(coll)
	if err != nil {
		return err
	}
	return e.db.Update(func(txn *badger.Txn) error {
		rec := shapeRecord{Fields: fields}
		if item, err := txn.Get(key); err == nil {
			raw, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			var have shapeRecord
			if err := json.Unmarshal(raw, &have); err != nil {
				return fmt.Errorf("cumulite: decode shape %s: %w", coll, err)
			}
			rec.Strict = have.Strict // re-declaring a shape keeps strictness
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return fmt.Errorf("cumulite: probe shape %s: %w", coll, err)
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("cumulite: encode shape %s: %w", coll, err)
		}
		if err := txn.Set(key, raw); err != nil {
			return fmt.Errorf("cumulite: set shape %s: %w", coll, err)
		}
		return nil
	})
}

// SetShapeStrict turns strict shape auditing on or off for a shaped
// collection. Strict, a write whose document misses required shape tags or
// carries unknown ones fails with ErrShapeViolation and nothing is stored;
// lenient, the write lands and the finding is remembered for LastShapeAudit.
// A collection with no declared shape cannot be armed — declare the shape
// first.
func (e *Engine) SetShapeStrict(ctx context.Context, coll string, strict bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := shapeKey(coll)
	if err != nil {
		return err
	}
	return e.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return notFoundf("shape %q", coll)
		}
		if err != nil {
			return fmt.Errorf("cumulite: probe shape %s: %w", coll, err)
		}
		raw, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		var rec shapeRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("cumulite: decode shape %s: %w", coll, err)
		}
		rec.Strict = strict
		updated, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("cumulite: encode shape %s: %w", coll, err)
		}
		if err := txn.Set(key, updated); err != nil {
			return fmt.Errorf("cumulite: set shape %s: %w", coll, err)
		}
		return nil
	})
}

// shapeOfTxn reads a collection's declared shape inside the caller's
// transaction, nil when none is declared (the unaudited common case).
func (e *Engine) shapeOfTxn(txn *badger.Txn, coll string) (*shapeRecord, error) {
	key, err := shapeKey(coll)
	if err != nil {
		return nil, err
	}
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cumulite: probe shape %s: %w", coll, err)
	}
	raw, err := item.ValueCopy(nil)
	if err != nil {
		return nil, err
	}
	var rec shapeRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("cumulite: decode shape %s: %w", coll, err)
	}
	return &rec, nil
}

// auditShape compares one document against a shape record. The audit is
// top-level only — the drift it exists for is a missing or renamed document
// key, and nesting the walk would turn the ruler itself into a schema language.
func auditShape(rec *shapeRecord, coll, id string, doc map[string]any) ShapeAudit {
	audit := ShapeAudit{Collection: coll, DocumentID: id, Strict: rec.Strict}
	claimed := make(map[string]bool, len(rec.Fields))
	for _, f := range rec.Fields {
		claimed[f.Name] = true
		if _, ok := doc[f.Name]; ok {
			continue
		}
		if f.Omitempty {
			audit.Absent = append(audit.Absent, f.Name)
		} else {
			audit.Missing = append(audit.Missing, f.Name)
		}
	}
	for k := range doc {
		if claimed[k] || k == "_id" || k == "_key" {
			continue // identity keys are the engine's bookkeeping, not drift
		}
		audit.Unknown = append(audit.Unknown, k)
	}
	sort.Strings(audit.Missing)
	sort.Strings(audit.Unknown)
	sort.Strings(audit.Absent)
	return audit
}

// auditDoc is the write-path hook: the audit of one document against the
// collection's declared shape, or nil when the collection has no shape.
func (e *Engine) auditDoc(txn *badger.Txn, coll, id string, doc map[string]any) (*ShapeAudit, error) {
	rec, err := e.shapeOfTxn(txn, coll)
	if err != nil || rec == nil {
		return nil, err
	}
	audit := auditShape(rec, coll, id, doc)
	return &audit, nil
}

// rememberShapeAudit keeps the most recent non-empty finding per collection,
// so lenient mode still leaves drift somewhere a test or an operator can find
// it instead of only in the stored bytes.
func (e *Engine) rememberShapeAudit(a ShapeAudit) {
	if a.empty() {
		return
	}
	e.auditMu.Lock()
	defer e.auditMu.Unlock()
	if e.lastAudit == nil {
		e.lastAudit = make(map[string]ShapeAudit)
	}
	e.lastAudit[a.Collection] = a
}

// LastShapeAudit reports the most recent non-empty shape finding for a
// collection and whether one exists. It is in-memory state, not durable: it
// covers the writes this engine process made, which is the window a parity
// test or a running service audits.
func (e *Engine) LastShapeAudit(coll string) (ShapeAudit, bool) {
	e.auditMu.Lock()
	defer e.auditMu.Unlock()
	a, ok := e.lastAudit[coll]
	return a, ok
}

// ShapeReport audits any document against a collection's declared shape
// without writing it — the ruler a parity test asserts with ("this struct's
// translation must produce every required key") and an ops check runs on a
// document read back from storage. The Absent field lists the document's
// missing omitempty keys; Missing and Unknown name drift. A collection with
// no declared shape has no ruler — that is a not-found error, not an empty
// audit.
func (e *Engine) ShapeReport(coll string, doc map[string]any) (ShapeAudit, error) {
	var rec *shapeRecord
	err := e.db.View(func(txn *badger.Txn) error {
		var err error
		rec, err = e.shapeOfTxn(txn, coll)
		return err
	})
	if err != nil {
		return ShapeAudit{}, err
	}
	if rec == nil {
		return ShapeAudit{}, notFoundf("shape %q", coll)
	}
	return auditShape(rec, coll, "", doc), nil
}
