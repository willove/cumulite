package cumulite

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/willove/cumulite/contract"
)

// The two sides of the fsync tradeoff, measured instead of assumed.
//
//  1. Committed writes survive process death. ask's fatal() path exits via
//     os.Exit, which skips the deferred Engine.Close(); this runs that exact
//     shape in a child process and counts what is readable afterwards.
//  2. Power-loss durability is a different promise and costs fsyncs. The second
//     test prints the per-transaction price on this machine, so the default is
//     a decision rather than a guess.

const durabilityChildEnv = "CUMULITE_DURABILITY_CHILD"

func TestAbruptExitKeepsCommittedWrites(t *testing.T) {
	if dir := os.Getenv(durabilityChildEnv); dir != "" {
		// Child: write, then die without Close — the fatal() shape.
		e, err := Open(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "child open:", err)
			os.Exit(1)
		}
		ctx := context.Background()
		if err := e.EnsureCollection(ctx, "c"); err != nil {
			os.Exit(1)
		}
		if _, err := e.Insert(ctx, "c", benchCorpus(50)); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAbruptExitKeepsCommittedWrites$")
	cmd.Env = append(os.Environ(), durabilityChildEnv+"="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	e, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	res, err := e.Query(context.Background(), "c", contract.Query{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Documents) != 50 {
		t.Fatalf("after an abrupt exit %d documents are readable, want 50: committed writes must survive os.Exit", len(res.Documents))
	}
}

func TestSyncWritesCost(t *testing.T) {
	const txns = 100
	measure := func(label string, sync bool) time.Duration {
		var opts []Option
		if sync {
			opts = append(opts, WithSyncWrites())
		}
		e, err := Open(t.TempDir(), opts...)
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()
		ctx := context.Background()
		if err := e.EnsureCollection(ctx, "c"); err != nil {
			t.Fatal(err)
		}
		docs := benchCorpus(txns)
		start := time.Now()
		for i := range docs {
			if _, err := e.Insert(ctx, "c", docs[i:i+1]); err != nil { // one txn per document, like ask's Put
				t.Fatal(err)
			}
		}
		d := time.Since(start)
		t.Logf("%-24s %d single-doc transactions in %8v (%.2f ms/txn)", label, txns, d.Round(time.Millisecond), float64(d.Microseconds())/1000/txns)
		return d
	}
	async := measure("SyncWrites=false", false)
	synced := measure("WithSyncWrites()", true)
	t.Logf("fsync price here: %.1fx — %.2f ms/txn without, %.2f ms/txn with",
		float64(synced)/float64(async),
		float64(async.Microseconds())/1000/txns, float64(synced.Microseconds())/1000/txns)
}
