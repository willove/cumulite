// Command cumulite is the lite engine's inspection and maintenance tool. It
// opens a store directory and answers questions about it: what is in there,
// does it decode, does the change log add up.
//
// It is deliberately not a server. The moment this binary grows a network
// surface it starts converging with cumudb, and "lite" stops meaning anything.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/willove/cumulite"
	"github.com/willove/cumulite/contract"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cumulite:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("no command given")
	}
	cmd, rest := args[0], args[1:]
	ctx := context.Background()
	switch cmd {
	case "health":
		return cmdHealth(ctx, rest)
	case "collections":
		return cmdCollections(ctx, rest)
	case "kv":
		return cmdKV(ctx, rest)
	case "doc":
		return cmdDoc(ctx, rest)
	case "index":
		return cmdIndex(ctx, rest)
	case "knn":
		return cmdKNN(ctx, rest)
	case "changes":
		return cmdChanges(ctx, rest)
	case "changelog":
		return cmdChangelog(ctx, rest)
	case "verify":
		return cmdVerify(ctx, rest)
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `cumulite — inspection and maintenance for a lite-engine store

usage: cumulite <command> [flags] ...

commands:
  health                              engine identity and uptime
  collections                         declared collections
  kv get KEY                          read one key
  kv put KEY VALUE [-ttl 30m]         write one key
  kv ls [PREFIX] [-limit N]           list keys under a prefix
  kv del KEY                          delete one key
  doc get COLL ID                      read one document
  doc ensure COLL                      declare a collection
  doc query COLL [-filter JSON] [-skip N] [-limit N]
  doc insert COLL FILE|-               insert a JSON array (or object) of documents
  doc patch COLL ID -set JSON          apply a $set patch
  doc del COLL ID                      delete one document
  index create COLL -field F [-dims N] [-metric cosine|l2|ip] [-model M]
  index ls COLL                        list a collection's vector indexes
  knn COLL -field F -vector 0.1,0.2 [-k 8] [-metric cosine] [-filter JSON]
  changes COLL [-cursor N] [-limit N]  read the change log after a cursor
  changelog COLL on|off               turn write recording on or off
  verify                               walk the keyspace, decode every value

flags:
  -data DIR        store directory (required unless -memory)
  -memory          open a throwaway in-memory store
`)
}

// openParsed opens the store from the two shared flags. -memory exists so a
// smoke run can exercise the binary without touching a directory.
func openParsed(dir string, memory bool) (*cumulite.Engine, error) {
	if memory {
		return cumulite.Open("", cumulite.WithInMemory())
	}
	if dir == "" {
		return nil, errors.New("-data DIR is required (or -memory)")
	}
	return cumulite.Open(dir)
}

func storeFlags(fs *flag.FlagSet) (*string, *bool) {
	return fs.String("data", "", "store directory"), fs.Bool("memory", false, "in-memory store")
}

func printJSON(v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Println(string(raw))
	return err
}

func cmdHealth(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("health", flag.ExitOnError)
	dir, mem := storeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openParsed(*dir, *mem)
	if err != nil {
		return err
	}
	defer e.Close()
	h, err := e.Health(ctx)
	if err != nil {
		return err
	}
	return printJSON(h)
}

func cmdCollections(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("collections", flag.ExitOnError)
	dir, mem := storeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openParsed(*dir, *mem)
	if err != nil {
		return err
	}
	defer e.Close()
	var names []string
	err = e.DB().View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte("m\x00")
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			names = append(names, string(it.Item().Key()[2:]))
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Println("(no declared collections)")
		return nil
	}
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

func cmdKV(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("kv needs a subcommand: get|put|ls|del")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("kv "+sub, flag.ExitOnError)
	dir, mem := storeFlags(fs)
	ttlRaw := fs.String("ttl", "", "time-to-live as a duration")
	limit := fs.Int("limit", 0, "max keys to list")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	e, err := openParsed(*dir, *mem)
	if err != nil {
		return err
	}
	defer e.Close()
	switch sub {
	case "get":
		if fs.NArg() < 1 {
			return errors.New("kv get KEY")
		}
		v, err := e.KVGet(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		os.Stdout.Write(v)
		fmt.Println()
		return nil
	case "put":
		if fs.NArg() < 2 {
			return errors.New("kv put KEY VALUE [-ttl 30m]")
		}
		var ttl time.Duration
		if *ttlRaw != "" {
			ttl, err = time.ParseDuration(*ttlRaw)
			if err != nil {
				return fmt.Errorf("-ttl %q: %w", *ttlRaw, err)
			}
		}
		return e.KVPut(ctx, fs.Arg(0), []byte(fs.Arg(1)), ttl)
	case "ls":
		prefix := ""
		if fs.NArg() > 0 {
			prefix = fs.Arg(0)
		}
		keys, err := e.KVKeys(ctx, prefix, *limit)
		if err != nil {
			return err
		}
		for _, k := range keys {
			fmt.Println(k)
		}
		if len(keys) == 0 {
			fmt.Println("(no keys)")
		}
		return nil
	case "del":
		if fs.NArg() < 1 {
			return errors.New("kv del KEY")
		}
		existed, err := e.KVDelete(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		fmt.Printf("deleted=%v\n", existed)
		return nil
	default:
		return fmt.Errorf("unknown kv subcommand %q", sub)
	}
}

func cmdDoc(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("doc needs a subcommand: get|ensure|query|insert|del")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("doc "+sub, flag.ExitOnError)
	dir, mem := storeFlags(fs)
	filterRaw := fs.String("filter", "", "filter as JSON")
	setRaw := fs.String("set", "", "fields as JSON for a $set patch")
	skip := fs.Int("skip", 0, "records to skip")
	limit := fs.Int("limit", 100, "page size")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	e, err := openParsed(*dir, *mem)
	if err != nil {
		return err
	}
	defer e.Close()
	switch sub {
	case "get":
		if fs.NArg() < 2 {
			return errors.New("doc get COLL ID")
		}
		d, err := e.GetDocument(ctx, fs.Arg(0), fs.Arg(1))
		if err != nil {
			return err
		}
		return printJSON(d)
	case "ensure":
		if fs.NArg() < 1 {
			return errors.New("doc ensure COLL")
		}
		return e.EnsureCollection(ctx, fs.Arg(0))
	case "query":
		if fs.NArg() < 1 {
			return errors.New("doc query COLL [-filter JSON] [-skip N] [-limit N]")
		}
		var q contract.Query
		if *filterRaw != "" {
			f, err := decodeJSONAny(*filterRaw)
			if err != nil {
				return fmt.Errorf("-filter: %w", err)
			}
			q.Filter = f
		}
		q.Skip, q.Limit = *skip, *limit
		res, err := e.Query(ctx, fs.Arg(0), q)
		if err != nil {
			return err
		}
		return printJSON(res)
	case "insert":
		if fs.NArg() < 2 {
			return errors.New("doc insert COLL FILE|-")
		}
		raw, err := readAllArg(fs.Arg(1))
		if err != nil {
			return err
		}
		docs, err := decodeDocs(raw)
		if err != nil {
			return err
		}
		ids, err := e.Insert(ctx, fs.Arg(0), docs)
		if err != nil {
			return err
		}
		fmt.Printf("inserted=%d\n", len(ids))
		return nil
	case "del":
		if fs.NArg() < 2 {
			return errors.New("doc del COLL ID")
		}
		existed, err := e.DeleteDocument(ctx, fs.Arg(0), fs.Arg(1))
		if err != nil {
			return err
		}
		fmt.Printf("deleted=%v\n", existed)
		return nil
	case "patch":
		if fs.NArg() < 2 {
			return errors.New("doc patch [-set JSON] COLL ID")
		}
		if *setRaw == "" {
			return errors.New(`doc patch needs -set '{"field": value}'`)
		}
		set, err := decodeJSONAny(*setRaw)
		if err != nil {
			return fmt.Errorf("-set: %w", err)
		}
		fields, ok := set.(map[string]any)
		if !ok {
			return errors.New("-set must be a JSON object")
		}
		d, err := e.PatchDocument(ctx, fs.Arg(0), fs.Arg(1), map[string]any{"$set": fields})
		if err != nil {
			return err
		}
		return printJSON(d)
	default:
		return fmt.Errorf("unknown doc subcommand %q", sub)
	}
}

func cmdIndex(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("index needs a subcommand: create|ls")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("index "+sub, flag.ExitOnError)
	dir, mem := storeFlags(fs)
	field := fs.String("field", "", "vector field")
	dims := fs.Int("dims", 0, "dimension count")
	metric := fs.String("metric", "cosine", "cosine|l2|ip")
	model := fs.String("model", "", "embedding model label")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	e, err := openParsed(*dir, *mem)
	if err != nil {
		return err
	}
	defer e.Close()
	switch sub {
	case "create":
		if fs.NArg() < 1 {
			return errors.New("index create COLL -field F [-dims N] [-metric cosine|l2|ip] [-model M]")
		}
		if *field == "" {
			return errors.New("-field is required")
		}
		return e.CreateIndexRequest(ctx, fs.Arg(0), contract.IndexRequest{
			Type: "vector", Field: *field, Dims: *dims, Metric: *metric, Model: *model,
		})
	case "ls":
		if fs.NArg() < 1 {
			return errors.New("index ls COLL")
		}
		return e.DB().View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.Prefix = []byte("i\x00" + fs.Arg(0) + "\x00")
			it := txn.NewIterator(opts)
			defer it.Close()
			for it.Rewind(); it.Valid(); it.Next() {
				raw, err := it.Item().ValueCopy(nil)
				if err != nil {
					return err
				}
				var def map[string]any
				if err := json.Unmarshal(raw, &def); err != nil {
					return fmt.Errorf("decode index %s: %w", it.Item().Key(), err)
				}
				if err := printJSON(def); err != nil {
					return err
				}
			}
			return nil
		})
	default:
		return fmt.Errorf("unknown index subcommand %q", sub)
	}
}

func cmdKNN(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("knn", flag.ExitOnError)
	dir, mem := storeFlags(fs)
	field := fs.String("field", "", "vector field")
	vector := fs.String("vector", "", "comma-separated query vector")
	k := fs.Int("k", 8, "neighbour count")
	metric := fs.String("metric", "", "cosine|l2|ip")
	filterRaw := fs.String("filter", "", "filter as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openParsed(*dir, *mem)
	if err != nil {
		return err
	}
	defer e.Close()
	if fs.NArg() < 1 {
		return errors.New("knn COLL -field F -vector 0.1,0.2")
	}
	if *field == "" || *vector == "" {
		return errors.New("-field and -vector are required")
	}
	vec, err := parseVector(*vector)
	if err != nil {
		return err
	}
	req := contract.KNNRequest{Field: *field, Vector: vec, K: *k, Metric: *metric}
	if *filterRaw != "" {
		f, err := decodeJSONAny(*filterRaw)
		if err != nil {
			return fmt.Errorf("-filter: %w", err)
		}
		req.Filter = f
	}
	res, err := e.KNN(ctx, fs.Arg(0), req)
	if err != nil {
		return err
	}
	return printJSON(res)
}

func cmdChanges(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("changes", flag.ExitOnError)
	dir, mem := storeFlags(fs)
	cursor := fs.Uint64("cursor", 0, "read records after this sequence")
	limit := fs.Int("limit", 100, "page size")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openParsed(*dir, *mem)
	if err != nil {
		return err
	}
	defer e.Close()
	if fs.NArg() < 1 {
		return errors.New("changes COLL [-cursor N] [-limit N]")
	}
	page, err := e.Changes(ctx, fs.Arg(0), *cursor, *limit)
	if err != nil {
		return err
	}
	return printJSON(page)
}

// cmdChangelog turns a collection's write recording on or off. Disabling
// stops new records; the records already written stay readable.
func cmdChangelog(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("changelog", flag.ExitOnError)
	dir, mem := storeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return errors.New("changelog [-data DIR] COLL on|off")
	}
	var enabled bool
	switch fs.Arg(1) {
	case "on", "true":
		enabled = true
	case "off", "false":
		enabled = false
	default:
		return fmt.Errorf("changelog state %q: want on|off", fs.Arg(1))
	}
	e, err := openParsed(*dir, *mem)
	if err != nil {
		return err
	}
	defer e.Close()
	return e.SetChangelog(ctx, fs.Arg(0), enabled)
}

// cmdVerify walks every key, decodes every value and checks each change log's
// counter against its last record. A store that fails here is damaged in a
// way reads alone would not show.
func cmdVerify(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	dir, mem := storeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openParsed(*dir, *mem)
	if err != nil {
		return err
	}
	defer e.Close()

	kinds := map[byte]int{}
	lastSeq := map[string]uint64{}
	var problems []string
	err = e.DB().View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := it.Item().Key()
			if len(key) < 2 {
				problems = append(problems, fmt.Sprintf("short key %q", key))
				continue
			}
			kinds[key[0]]++
			raw, err := it.Item().ValueCopy(nil)
			if err != nil {
				problems = append(problems, fmt.Sprintf("read %q: %v", key, err))
				continue
			}
			switch key[0] {
			case 'd', 'i', 'c':
				var v any
				if err := json.Unmarshal(raw, &v); err != nil {
					problems = append(problems, fmt.Sprintf("decode %q: %v", key, err))
				}
				if key[0] == 'c' {
					coll, seq := splitChangeKey(string(key[2:]))
					lastSeq[coll] = seq // keys ascend, so the last one wins
				}
			case 'v':
				if len(raw) == 0 || len(raw)%4 != 0 {
					problems = append(problems, fmt.Sprintf("vector %q: %d bytes is not a multiple of 4", key, len(raw)))
				}
			case 'n':
				if len(raw) != 8 {
					problems = append(problems, fmt.Sprintf("change counter %q: %d bytes, want 8", key, len(raw)))
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	names := map[byte]string{
		'd': "documents", 'v': "vectors", 'i': "indexes", 'c': "change records",
		'e': "changelog flags", 'n': "change counters", 'k': "kv entries", 'm': "collection markers",
	}
	fmt.Println("keyspace:")
	for _, kind := range []byte{'d', 'v', 'i', 'c', 'e', 'n', 'k', 'm'} {
		if kinds[kind] > 0 {
			fmt.Printf("  %-16s %d\n", names[kind], kinds[kind])
		}
	}

	// Change-log arithmetic: each collection's counter must equal the sequence
	// of its last record. A mismatch means a write recorded without its
	// counter, or the reverse — the reader would skip or repeat records.
	colls := make([]string, 0, len(lastSeq))
	for c := range lastSeq {
		colls = append(colls, c)
	}
	sort.Strings(colls)
	for _, coll := range colls {
		var counter uint64
		viewErr := e.DB().View(func(txn *badger.Txn) error {
			it, err := txn.Get([]byte("n\x00" + coll))
			if err != nil {
				return err
			}
			raw, err := it.ValueCopy(nil)
			if err != nil {
				return err
			}
			if len(raw) == 8 {
				counter = uint64(raw[0])<<56 | uint64(raw[1])<<48 | uint64(raw[2])<<40 | uint64(raw[3])<<32 |
					uint64(raw[4])<<24 | uint64(raw[5])<<16 | uint64(raw[6])<<8 | uint64(raw[7])
			}
			return nil
		})
		if viewErr != nil {
			problems = append(problems, fmt.Sprintf("counter %s: %v", coll, viewErr))
			continue
		}
		if counter != lastSeq[coll] {
			problems = append(problems, fmt.Sprintf("changelog %s: counter=%d last record=%d", coll, counter, lastSeq[coll]))
		}
	}

	if len(problems) == 0 {
		fmt.Println("verify: OK")
		return nil
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, "problem:", p)
	}
	return fmt.Errorf("verify found %d problem(s)", len(problems))
}

// splitChangeKey splits "coll\x00%016d" back into its parts.
func splitChangeKey(s string) (string, uint64) {
	i := strings.LastIndex(s, "\x00")
	if i < 0 {
		return s, 0
	}
	seq, _ := strconv.ParseUint(s[i+1:], 10, 64)
	return s[:i], seq
}

func readAllArg(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

// decodeDocs reads a JSON array of documents, or a single document object.
func decodeDocs(raw []byte) ([]map[string]any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var docs []map[string]any
		if err := json.Unmarshal([]byte(trimmed), &docs); err != nil {
			return nil, fmt.Errorf("decode document array: %w", err)
		}
		return docs, nil
	}
	var one map[string]any
	if err := json.Unmarshal([]byte(trimmed), &one); err != nil {
		return nil, fmt.Errorf("decode document: %w", err)
	}
	return []map[string]any{one}, nil
}

func decodeJSONAny(raw string) (any, error) {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, fmt.Errorf("%q is not JSON", raw)
	}
	return v, nil
}

func parseVector(s string) ([]float64, error) {
	parts := strings.Split(s, ",")
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, fmt.Errorf("-vector %q: %q is not a number", s, p)
		}
		out = append(out, f)
	}
	return out, nil
}
