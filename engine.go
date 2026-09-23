package cumulite

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/willove/cumudb/pkg/client"
	"github.com/dgraph-io/badger/v4"
)

// Engine is an embedded CumuDB-lite: one Badger store on disk (or in memory)
// serving the Port contract. It is safe for concurrent use; every write is one
// Badger transaction covering the document, its vectors and its change-log
// records, so a reader never sees a half-applied write.
//
// Key layout (all keys are raw bytes, \x00 separates segments — collection
// names and ids containing it are refused):
//
//	d\x00<coll>\x00<id>            document, JSON
//	v\x00<coll>\x00<field>\x00<id> vector, little-endian float32
//	i\x00<coll>\x00<name>          vector index definition, JSON
//	c\x00<coll>\x00<seq>           change record, JSON (seq is %016d)
//	e\x00<coll>                    change log enabled flag, 1 byte
//	n\x00<coll>                    next change sequence, 8-byte big-endian
//	k\x00<key>                     key/value entry, raw
type Engine struct {
	db    *badger.DB
	start time.Time
}

// Option configures an Engine.
type Option func(*options)

type options struct {
	inMemory   bool
	syncWrites bool
}

// WithInMemory keeps the whole store in RAM (tests; no directory touched).
func WithInMemory() Option { return func(o *options) { o.inMemory = true } }

// WithSyncWrites fsyncs every commit. Off by default — Badger's value log
// survives process death without it, and only a machine crash can cost the
// tail writes.
func WithSyncWrites() Option { return func(o *options) { o.syncWrites = true } }

// nopLogger silences Badger's per-compaction INFO chatter.
type nopLogger struct{}

func (nopLogger) Errorf(string, ...any)   {}
func (nopLogger) Warningf(string, ...any) {}
func (nopLogger) Infof(string, ...any)    {}
func (nopLogger) Debugf(string, ...any)   {}

// Open creates or opens a store at dir (WithInMemory ignores the directory).
func Open(dir string, opts ...Option) (*Engine, error) {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	if dir == "" && !o.inMemory {
		return nil, errors.New("cumulite: open: empty directory")
	}
	bopts := badger.DefaultOptions(dir).WithLogger(nopLogger{})
	if o.inMemory {
		bopts = badger.DefaultOptions("").WithInMemory(true).WithLogger(nopLogger{})
	}
	bopts.SyncWrites = o.syncWrites
	db, err := badger.Open(bopts)
	if err != nil {
		return nil, fmt.Errorf("cumulite: open %s: %w", dir, err)
	}
	return &Engine{db: db, start: time.Now()}, nil
}

// Close flushes and closes the store.
func (e *Engine) Close() error { return e.db.Close() }

// DB exposes the underlying Badger handle for operators (compaction, backup).
func (e *Engine) DB() *badger.DB { return e.db }

// ---------------------------------------------------------------------------
// Key encoding
// ---------------------------------------------------------------------------

func cleanSegment(kind, s string) (string, error) {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return "", fmt.Errorf("cumulite: %s %q contains a NUL byte", kind, s)
		}
	}
	return s, nil
}

func docPrefix(coll string) ([]byte, error) {
	c, err := cleanSegment("collection", coll)
	if err != nil {
		return nil, err
	}
	return []byte("d\x00" + c + "\x00"), nil
}

func vecPrefix(coll, field string) ([]byte, error) {
	c, err := cleanSegment("collection", coll)
	if err != nil {
		return nil, err
	}
	f, err := cleanSegment("field", field)
	if err != nil {
		return nil, err
	}
	return []byte("v\x00" + c + "\x00" + f + "\x00"), nil
}

func indexPrefix(coll string) ([]byte, error) {
	c, err := cleanSegment("collection", coll)
	if err != nil {
		return nil, err
	}
	return []byte("i\x00" + c + "\x00"), nil
}

func changePrefix(coll string) ([]byte, error) {
	c, err := cleanSegment("collection", coll)
	if err != nil {
		return nil, err
	}
	return []byte("c\x00" + c + "\x00"), nil
}

func changelogEnabledKey(coll string) ([]byte, error) {
	c, err := cleanSegment("collection", coll)
	if err != nil {
		return nil, err
	}
	return []byte("e\x00" + c), nil
}

func kvKey(key string) []byte { return []byte("k\x00" + key) }

const kvPrefixLen = 2 // len("k\x00")

func docKey(coll, id string) ([]byte, error) {
	prefix, err := docPrefix(coll)
	if err != nil {
		return nil, err
	}
	d, err := cleanSegment("id", id)
	if err != nil {
		return nil, err
	}
	return append(prefix, d...), nil
}

func vecKey(coll, field, id string) ([]byte, error) {
	prefix, err := vecPrefix(coll, field)
	if err != nil {
		return nil, err
	}
	d, err := cleanSegment("id", id)
	if err != nil {
		return nil, err
	}
	return append(prefix, d...), nil
}

func indexKey(coll, name string) ([]byte, error) {
	prefix, err := indexPrefix(coll)
	if err != nil {
		return nil, err
	}
	n, err := cleanSegment("index", name)
	if err != nil {
		return nil, err
	}
	return append(prefix, n...), nil
}

func changeKey(coll string, seq uint64) ([]byte, error) {
	prefix, err := changePrefix(coll)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("%s%016d", prefix, seq)), nil
}

func seqCounterKey(coll string) ([]byte, error) {
	c, err := cleanSegment("collection", coll)
	if err != nil {
		return nil, err
	}
	return []byte("n\x00" + c), nil
}

// ensureMarker records a declared collection. The server refuses writes to
// undeclared collections (a typo raises COLLECTION_NOT_FOUND, not a new empty
// collection), so the lite engine keeps the same guard: schemaless storage
// still needs the declaration to exist as an explicit fact.
func ensureMarkerKey(coll string) ([]byte, error) {
	c, err := cleanSegment("collection", coll)
	if err != nil {
		return nil, err
	}
	return []byte("m\x00" + c), nil
}

func (e *Engine) collectionDeclared(txn *badger.Txn, coll string) (bool, error) {
	key, err := ensureMarkerKey(coll)
	if err != nil {
		return false, err
	}
	_, err = txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cumulite: probe collection %s: %w", coll, err)
	}
	return true, nil
}

func collNotFound(coll string) error {
	return notFoundf("collection %q", coll)
}

func encodeU64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func decodeU64(b []byte) uint64 {
	if len(b) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

// runUpdate retries a read-modify-write transaction on Badger's conflict
// detection: two writers touching the same keys lose one race, and the loser
// replays.
func (e *Engine) runUpdate(fn func(*badger.Txn) error) error {
	var err error
	for attempt := 0; attempt < 64; attempt++ {
		if err = e.db.Update(fn); err == nil || !errors.Is(err, badger.ErrConflict) {
			return err
		}
	}
	return err
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

// Health reports engine identity and uptime. It touches no storage — a store
// that answers this is answering storage.
func (e *Engine) Health(_ context.Context) (client.Health, error) {
	return client.Health{
		Status:    "ok",
		Version:   "cumulite/0.1.0",
		Backend:   "badger",
		InMemory:  e.db.Opts().InMemory,
		UptimeSec: int(time.Since(e.start).Seconds()),
	}, nil
}

// ---------------------------------------------------------------------------
// Store helpers
// ---------------------------------------------------------------------------

// getStored reads one document, returning a not-found error when absent.
func (e *Engine) getStored(txn *badger.Txn, coll, id string) (map[string]any, error) {
	key, err := docKey(coll, id)
	if err != nil {
		return nil, err
	}
	item, err := txn.Get(key)
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, notFoundf("document %s/%s", coll, id)
		}
		return nil, fmt.Errorf("cumulite: get %s/%s: %w", coll, id, err)
	}
	raw, err := item.ValueCopy(nil)
	if err != nil {
		return nil, fmt.Errorf("cumulite: read %s/%s: %w", coll, id, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("cumulite: decode %s/%s: %w", coll, id, err)
	}
	return doc, nil
}

// putStored writes one document, its vectors and (when recording) its change
// record inside the caller's transaction.
func (e *Engine) putStored(txn *badger.Txn, coll, id string, doc map[string]any, op string) error {
	key, err := docKey(coll, id)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("cumulite: encode %s/%s: %w", coll, id, err)
	}
	if err := txn.Set(key, raw); err != nil {
		return fmt.Errorf("cumulite: set %s/%s: %w", coll, id, err)
	}
	if err := e.syncVectors(txn, coll, id, doc); err != nil {
		return err
	}
	recording, err := e.changelogEnabled(txn, coll)
	if err != nil {
		return err
	}
	if !recording {
		return nil
	}
	return e.appendChange(txn, coll, id, op)
}

// syncVectors mirrors the document's vector fields into the vector keyspace,
// dropping the keys of fields that disappeared. Vec32 halves the storage of a
// 384-dim vector; cosine over float32 matches float64 to seven digits, well
// past the resolution any ranking here uses.
func (e *Engine) syncVectors(txn *badger.Txn, coll, id string, doc map[string]any) error {
	indexes, err := e.listIndexesTxn(txn, coll)
	if err != nil {
		return err
	}
	for _, idx := range indexes {
		vkey, err := vecKey(coll, idx.Field, id)
		if err != nil {
			return err
		}
		vec, ok := vectorField(doc, idx.Field)
		if !ok {
			_ = txn.Delete(vkey)
			continue
		}
		if idx.Dims > 0 && len(vec) != idx.Dims {
			return fmt.Errorf("%w: field %s has %d, index %s declares %d",
				errDimsMismat, idx.Field, len(vec), idx.Name, idx.Dims)
		}
		if err := txn.Set(vkey, encodeVec32(vec)); err != nil {
			return fmt.Errorf("cumulite: set vector %s/%s: %w", coll, id, err)
		}
	}
	return nil
}

// appendChange records one write in the collection's change log, allocating
// the sequence inside the same transaction as the write it describes.
func (e *Engine) appendChange(txn *badger.Txn, coll, id, op string) error {
	key, err := seqCounterKey(coll)
	if err != nil {
		return err
	}
	next := uint64(1)
	if item, err := txn.Get(key); err == nil {
		raw, cerr := item.ValueCopy(nil)
		if cerr != nil {
			return cerr
		}
		next = decodeU64(raw) + 1
	} else if !errors.Is(err, badger.ErrKeyNotFound) {
		return fmt.Errorf("cumulite: read change sequence %s: %w", coll, err)
	}
	if err := txn.Set(key, encodeU64(next)); err != nil {
		return fmt.Errorf("cumulite: bump change sequence %s: %w", coll, err)
	}
	rec := client.ChangeRecord{
		Sequence: next,
		Op:       op,
		ID:       id,
		Created:  op == "insert",
		At:       time.Now().UTC(),
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("cumulite: encode change %s/%s: %w", coll, id, err)
	}
	ckey, err := changeKey(coll, next)
	if err != nil {
		return err
	}
	return txn.Set(ckey, raw)
}

// changelogEnabled reads the recording flag (false when never set).
func (e *Engine) changelogEnabled(txn *badger.Txn, coll string) (bool, error) {
	key, err := changelogEnabledKey(coll)
	if err != nil {
		return false, err
	}
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cumulite: read changelog flag %s: %w", coll, err)
	}
	raw, err := item.ValueCopy(nil)
	if err != nil {
		return false, err
	}
	return len(raw) > 0 && raw[0] == 1, nil
}

// vectorField reads a float64 vector field, tolerating []any (JSON-decoded)
// and []float32 spellings.
func vectorField(doc map[string]any, field string) ([]float64, bool) {
	v, ok := doc[field]
	if !ok {
		return nil, false
	}
	switch typed := v.(type) {
	case []float64:
		return typed, len(typed) > 0
	case []float32:
		out := make([]float64, len(typed))
		for i, f := range typed {
			out[i] = float64(f)
		}
		return out, len(out) > 0
	case []any:
		out := make([]float64, 0, len(typed))
		for _, item := range typed {
			f, ok := toFloat(item)
			if !ok {
				return nil, false
			}
			out = append(out, f)
		}
		return out, len(out) > 0
	}
	return nil, false
}

func encodeVec32(v []float64) []byte {
	out := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(float32(f)))
	}
	return out
}

func decodeVec32(raw []byte) []float64 {
	n := len(raw) / 4
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:])))
	}
	return out
}
