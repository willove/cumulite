package main

import (
	"flag"
	"reflect"
	"testing"
)

// The usage text documents the collection first (`doc query COLL -filter JSON`).
// The stdlib FlagSet stops at that first positional, so these cases are the
// difference between a filter being applied and being silently dropped.
func TestFlagFirst(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"collection first", []string{"ask_sources", "-filter", `{"a":1}`}, []string{"-filter", `{"a":1}`, "ask_sources"}},
		{"flags first unchanged", []string{"-filter", `{"a":1}`, "ask_sources"}, []string{"-filter", `{"a":1}`, "ask_sources"}},
		{"bool flag takes no value", []string{"ask_sources", "-memory"}, []string{"-memory", "ask_sources"}},
		{"equals form carries its own value", []string{"ask_sources", "-limit=5"}, []string{"-limit=5", "ask_sources"}},
		{"positionals keep their order", []string{"ask_sources", "src:1", "-set", `{"a":1}`}, []string{"-set", `{"a":1}`, "ask_sources", "src:1"}},
		{"terminator ends flag scanning", []string{"-limit", "5", "--", "-not-a-flag"}, []string{"-limit", "5", "-not-a-flag"}},
		{"unknown flag is handed through", []string{"ask_sources", "-nope", "x"}, []string{"-nope", "ask_sources", "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			fs.String("data", "", "")
			fs.Bool("memory", false, "")
			fs.String("filter", "", "")
			fs.String("set", "", "")
			fs.Int("limit", 0, "")
			if got := flagFirst(fs, tc.args); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("flagFirst(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// End to end through a real FlagSet: the collection-first form must leave the
// flags parsed and the positional intact.
func TestParseArgsCollectionFirst(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("data", "", "")
	filter := fs.String("filter", "", "")
	limit := fs.Int("limit", 100, "")
	if err := parseArgs(fs, []string{"ask_sources", "-filter", `{"status":"live"}`, "-limit", "5"}); err != nil {
		t.Fatal(err)
	}
	if *filter != `{"status":"live"}` {
		t.Fatalf("filter = %q, want it parsed rather than dropped", *filter)
	}
	if *limit != 5 {
		t.Fatalf("limit = %d, want 5", *limit)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "ask_sources" {
		t.Fatalf("positionals = %v, want [ask_sources]", fs.Args())
	}
}

func TestParseArgsRejectsUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("filter", "", "")
	if err := parseArgs(fs, []string{"COLL", "-nope", "x"}); err == nil {
		t.Fatal("an unknown flag after a positional must fail, not be swallowed as a positional")
	}
}
