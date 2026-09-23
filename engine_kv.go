package cumulite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/willove/cumulite/contract"
)

// KVPut stores a value. A positive TTL rides on Badger's own expiry, so a
// lapsed key reads back as absent without a sweeper.
func (e *Engine) KVPut(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
func (e *Engine) KVGet(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
func (e *Engine) KVDelete(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
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
func (e *Engine) KVKeys(ctx context.Context, prefix string, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []string{}
	err := e.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte("k\x00")
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
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
func (e *Engine) SetChangelog(ctx context.Context, coll string, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
func (e *Engine) Changes(ctx context.Context, coll string, cursor uint64, limit int) (*contract.ChangesPage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	prefix, err := changePrefix(coll)
	if err != nil {
		return nil, err
	}
	var (
		changes []contract.ChangeRecord
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
			if err := ctx.Err(); err != nil {
				return err
			}
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
			var rec contract.ChangeRecord
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
	return &contract.ChangesPage{
		Changes: changes,
		Count:   len(changes),
		Cursor:  next,
		Enabled: enabled,
	}, nil
}
