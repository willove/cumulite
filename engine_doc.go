package cumulite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dgraph-io/badger/v4"
)

// EnsureCollection is the declaration the server requires before a first
// write: it is idempotent, and once written, the collection exists. The
// storage stays schemaless — the marker is the declaration, not a shape.
func (e *Engine) EnsureCollection(ctx context.Context, coll string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := ensureMarkerKey(coll)
	if err != nil {
		return err
	}
	return e.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, []byte{1})
	})
}

// Insert stores documents as one transaction, resolving each identity from
// _id (or _key) or generating one. An existing key fails the batch — the
// caller's "insert or detect prior presence" idiom — and nothing is written.
// A collection that was never declared fails the batch the way the server
// does, so a typo'd identity cannot silently become a new empty collection.
func (e *Engine) Insert(ctx context.Context, coll string, documents []map[string]any) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(documents) == 0 {
		return nil, nil
	}
	type resolved struct {
		id  string
		doc map[string]any
	}
	items := make([]resolved, 0, len(documents))
	for _, doc := range documents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if doc == nil {
			return nil, errors.New("cumulite: nil document")
		}
		id, err := docID(doc)
		if err != nil {
			return nil, err
		}
		items = append(items, resolved{id: id, doc: doc})
	}
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.id
	}
	err := e.runUpdate(func(txn *badger.Txn) error {
		declared, err := e.collectionDeclared(txn, coll)
		if err != nil {
			return err
		}
		if !declared {
			return collNotFound(coll)
		}
		for _, it := range items {
			key, err := docKey(coll, it.id)
			if err != nil {
				return err
			}
			if _, err := txn.Get(key); err == nil {
				return fmt.Errorf("%w: %s/%s", ErrDuplicate, coll, it.id)
			} else if !errors.Is(err, badger.ErrKeyNotFound) {
				return fmt.Errorf("cumulite: probe %s/%s: %w", coll, it.id, err)
			}
		}
		for _, it := range items {
			stored := it.doc
			if _, ok := stored["_id"]; !ok {
				stored = cloneDoc(it.doc)
				stored["_id"] = it.id
			}
			if err := e.putStored(txn, coll, it.id, stored, "insert"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// GetDocument returns one document or a not-found error.
func (e *Engine) GetDocument(ctx context.Context, coll, id string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out map[string]any
	err := e.db.View(func(txn *badger.Txn) error {
		doc, err := e.getStored(txn, coll, id)
		if err != nil {
			return err
		}
		out = doc
		return nil
	})
	return out, err
}

// ReplaceDocument stores the full new state of an existing document; a missing
// document is a not-found error, which is how the graph store tells insert
// from update.
func (e *Engine) ReplaceDocument(ctx context.Context, coll, id string, document map[string]any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if document == nil {
		return nil, errors.New("cumulite: nil document")
	}
	var out map[string]any
	err := e.runUpdate(func(txn *badger.Txn) error {
		if _, err := e.getStored(txn, coll, id); err != nil {
			return err
		}
		if document["_id"] == nil {
			document = cloneDoc(document)
			document["_id"] = id
		}
		if err := e.putStored(txn, coll, id, document, "replace"); err != nil {
			return err
		}
		stored, err := e.getStored(txn, coll, id)
		if err != nil {
			return err
		}
		out = stored
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PatchDocument applies the $set operator and returns the stored document.
// The operators beyond $set are refused rather than ignored — a silently
// dropped $unset would leave stale fields behind.
func (e *Engine) PatchDocument(ctx context.Context, coll, id string, update map[string]any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if update == nil {
		return nil, errors.New("cumulite: nil update")
	}
	set, err := extractSet(update)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = e.runUpdate(func(txn *badger.Txn) error {
		current, err := e.getStored(txn, coll, id)
		if err != nil {
			return err
		}
		for k, v := range set {
			current[k] = v
		}
		if err := e.putStored(txn, coll, id, current, "patch"); err != nil {
			return err
		}
		out = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteDocument removes a document, its vectors and (when recording) its
// change record, reporting whether it existed.
func (e *Engine) DeleteDocument(ctx context.Context, coll, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	existed := false
	err := e.runUpdate(func(txn *badger.Txn) error {
		key, err := docKey(coll, id)
		if err != nil {
			return err
		}
		if _, err := txn.Get(key); err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return nil
			}
			return fmt.Errorf("cumulite: probe %s/%s: %w", coll, id, err)
		}
		existed = true
		if err := txn.Delete(key); err != nil {
			return fmt.Errorf("cumulite: delete %s/%s: %w", coll, id, err)
		}
		indexes, err := e.listIndexesTxn(txn, coll)
		if err != nil {
			return err
		}
		for _, idx := range indexes {
			vkey, err := vecKey(coll, idx.Field, id)
			if err != nil {
				return err
			}
			_ = txn.Delete(vkey)
		}
		recording, err := e.changelogEnabled(txn, coll)
		if err != nil {
			return err
		}
		if recording {
			if err := e.appendChange(txn, coll, id, "delete"); err != nil {
				return err
			}
		}
		return nil
	})
	return existed, err
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// docID resolves a document's identity: _id wins, _key is the fallback, and
// an absent one is generated (callers that care supply _id).
func docID(doc map[string]any) (string, error) {
	if v, ok := doc["_id"].(string); ok && v != "" {
		return v, nil
	}
	if v, ok := doc["_key"].(string); ok && v != "" {
		return v, nil
	}
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("cumulite: generate id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// cloneDoc deep-copies through JSON so a write never aliases caller state.
func cloneDoc(doc map[string]any) map[string]any {
	raw, err := json.Marshal(doc)
	if err != nil {
		return doc
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return doc
	}
	return out
}

// extractSet reads {"$set": {...}}, refusing every other operator.
func extractSet(update map[string]any) (map[string]any, error) {
	if set, ok := update["$set"].(map[string]any); ok {
		if len(update) != 1 {
			return nil, fmt.Errorf("cumulite: patch mixes $set with fields: %v", update)
		}
		return set, nil
	}
	for k := range update {
		if len(k) > 0 && k[0] == '$' {
			return nil, fmt.Errorf("cumulite: unsupported patch operator %q", k)
		}
	}
	return nil, errors.New(`cumulite: patch carries no "$set"`)
}
