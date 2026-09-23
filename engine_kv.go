package cumulite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/willove/cumudb/pkg/client"
	"github.com/dgraph-io/badger/v4"
)

// KVPut stores a value. A positive TTL rides on Badger's own expiry, so a
// lapsed key reads back as absent without a sweeper.
func (e *Engine) KVPut(_ context.Context, key string, value []byte, ttl time.Duration) error {
	return e.db.Update(func(txn *badger.Txn) error {
		entry := badger.NewEntry(kvKey(key), value)
		if ttl > 0 {
			entry = entry.WithTTL(ttl)
		}
		if err := txn.SetEntry(entry); err != nil {
			return fmt.Errorf("cumulite: kv put %q: %w", key, err)
		}
		return nil
	})
}

// KVGet returns a value or a not-found error.
func (e *Engine) KVGet(_ context.Context, key string) ([]byte, error) {
	var out []byte
	err := e.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(kvKey(key))
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return notFoundf("key %q", key)
			}
			return fmt.Errorf("cumulite: kv get %q: %w", key, err)
		}
		out, err = item.ValueCopy(nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// KVDelete removes a key, reporting whether it existed.
func (e *Engine) KVDelete(_ context.Context, key string) (bool, error) {
	existed := false
	err := e.db.Update(func(txn *badger.Txn) error {
		if _, err := txn.Get(kvKey(key)); err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return nil
			}
			return fmt.Errorf("cumulite: kv probe %q: %w", key, err)
		}
		existed = true
		return txn.Delete(kvKey(key))
	})
	return existed, err
}

// KVKeys lists the keys under a prefix in key order, capped at limit.
func (e *Engine) KVKeys(_ context.Context, prefix string, limit int) ([]string, error) {
	out := []string{}
	err := e.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte("k\x00")
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			key := string(it.Item().Key()[kvPrefixLen:])
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			out = append(out, key)
			if limit > 0 && len(out) >= limit {
				return nil
			}
		}
		return nil
	})
	return out, err
}

// SetChangelog turns a collection's write recording on or off. Disabling stops
// new records; the records already written stay readable.
func (e *Engine) SetChangelog(_ context.Context, coll string, enabled bool) error {
	key, err := changelogEnabledKey(coll)
	if err != nil {
		return err
	}
	flag := []byte{0}
	if enabled {
		flag[0] = 1
	}
	return e.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, flag)
	})
}

// Changes reads the records after a sequence cursor, oldest first. The
// returned cursor is the last sequence returned, or the cursor the read
// started from when nothing followed it — the value a reader persists to make
// at-least-once delivery converge.
func (e *Engine) Changes(_ context.Context, coll string, cursor uint64, limit int) (*client.ChangesPage, error) {
	if limit <= 0 {
		limit = 100
	}
	prefix, err := changePrefix(coll)
	if err != nil {
		return nil, err
	}
	var (
		changes []client.ChangeRecord
		next    = cursor
		enabled bool
	)
	err = e.db.View(func(txn *badger.Txn) error {
		on, err := e.changelogEnabled(txn, coll)
		if err != nil {
			return err
		}
		enabled = on
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		if cursor > 0 {
			seek, err := changeKey(coll, cursor)
			if err != nil {
				return err
			}
			it.Seek(seek)
		} else {
			it.Rewind()
		}
		for ; it.Valid(); it.Next() {
			item := it.Item()
			if !it.ValidForPrefix(prefix) {
				break
			}
			if item.IsDeletedOrExpired() {
				continue
			}
			raw, err := item.ValueCopy(nil)
			if err != nil {
				return fmt.Errorf("cumulite: read change %s: %w", coll, err)
			}
			var rec client.ChangeRecord
			if err := json.Unmarshal(raw, &rec); err != nil {
				return fmt.Errorf("cumulite: decode change %s: %w", coll, err)
			}
			if rec.Sequence <= cursor {
				continue
			}
			changes = append(changes, rec)
			next = rec.Sequence
			if len(changes) >= limit {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &client.ChangesPage{
		Changes: changes,
		Count:   len(changes),
		Cursor:  next,
		Enabled: enabled,
	}, nil
}
