package cumulite

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/willove/cumulite/contract"
)

// The domain-ish struct the drift tests share. judge_ok is the field the real
// incident lost: the consumer's hand-built translation table never grew it,
// the map path stored documents without it, and reads hid the loss.
type clusterDoc struct {
	Name    string    `json:"name"`
	JudgeOK bool      `json:"judge_ok"`
	Retired *bool     `json:"retired,omitempty"` // nil → absent, like a conditional write
	Note    string    `json:"note,omitempty"`
	At      time.Time `json:"at"`
}

func mustSetShape(t *testing.T, e *Engine, coll string, shape any) {
	t.Helper()
	if err := e.SetCollectionShape(context.Background(), coll, shape); err != nil {
		t.Fatalf("set shape: %v", err)
	}
}

func rfc3339(t *testing.T) string {
	t.Helper()
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func TestInsertStructsKeepsJsonTags(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()
	mustEnsure(t, e, "c")

	retired := true
	at := time.Date(2026, 9, 27, 12, 0, 0, 123000000, time.UTC)
	ids, err := e.InsertStructs(ctx, "c", []any{clusterDoc{Name: "c1", JudgeOK: true, Retired: &retired, At: at}})
	if err != nil {
		t.Fatalf("insert structs: %v", err)
	}
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("ids = %v", ids)
	}

	doc, err := e.GetDocument(ctx, "c", ids[0])
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if doc["name"] != "c1" || doc["judge_ok"] != true {
		t.Fatalf("tags lost: %v", doc)
	}
	if doc["retired"] != true {
		t.Fatalf("pointer field: %v", doc)
	}
	if doc["note"] != nil {
		t.Fatalf("empty omitempty must be absent, got %v", doc["note"])
	}
	if got, want := doc["at"], at.Format(time.RFC3339Nano); got != want {
		t.Fatalf("time = %v, want RFC3339Nano %v", got, want)
	}

	// A nil pointer with omitempty behaves like the hand-written conditional
	// write it replaces.
	ids, err = e.InsertStructs(ctx, "c", []any{clusterDoc{Name: "c2", JudgeOK: false, At: at}})
	if err != nil {
		t.Fatalf("insert structs 2: %v", err)
	}
	if doc, err = e.GetDocument(ctx, "c", ids[0]); err != nil {
		t.Fatalf("get 2: %v", err)
	}
	if _, ok := doc["retired"]; ok {
		t.Fatalf("nil pointer must be absent: %v", doc)
	}

	// ReplaceStruct is the typed spelling of the full-state replace.
	out, err := e.ReplaceStruct(ctx, "c", ids[0], clusterDoc{Name: "c1", JudgeOK: false, Note: "replaced", At: at})
	if err != nil {
		t.Fatalf("replace struct: %v", err)
	}
	if out["judge_ok"] != false || out["note"] != "replaced" {
		t.Fatalf("replaced doc = %v", out)
	}
	if _, ok := out["retired"]; ok {
		t.Fatalf("replace must drop the absent pointer field: %v", out)
	}

	// _id from a json tag is honoured as identity, as in the map path.
	type ided struct {
		ID string `json:"_id"`
		N  int    `json:"n"`
	}
	got, err := e.InsertStructs(ctx, "c", []any{ided{ID: "fixed", N: 7}})
	if err != nil {
		t.Fatalf("insert ided: %v", err)
	}
	if got[0] != "fixed" {
		t.Fatalf("id = %q, want fixed", got[0])
	}
	if _, err := e.InsertStructs(ctx, "c", []any{ided{ID: "fixed", N: 8}}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate err = %v, want ErrDuplicate", err)
	}
}

func TestShapeAuditCatchesTranslationDrift(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()
	mustEnsure(t, e, "clusters")
	mustSetShape(t, e, "clusters", clusterDoc{})

	// The incident, replayed: a hand-built map that forgot judge_ok. Lenient
	// mode stores it — but the drift is now a finding, not a silence.
	hand := map[string]any{"name": "c1", "at": rfc3339(t)}
	ids, err := e.Insert(ctx, "clusters", []map[string]any{hand})
	if err != nil {
		t.Fatalf("insert drifted: %v", err)
	}

	last, ok := e.LastShapeAudit("clusters")
	if !ok {
		t.Fatal("drifted write left no audit")
	}
	if !slices.Equal(last.Missing, []string{"judge_ok"}) {
		t.Fatalf("missing = %v, want [judge_ok]", last.Missing)
	}
	if last.DocumentID != ids[0] || last.Collection != "clusters" {
		t.Fatalf("audit ids = %s/%s", last.Collection, last.DocumentID)
	}

	// ShapeReport is the same ruler without a write: parity tests assert
	// against it before anything reaches storage.
	report, err := e.ShapeReport("clusters", hand)
	if err != nil {
		t.Fatalf("shape report: %v", err)
	}
	if !slices.Equal(report.Missing, []string{"judge_ok"}) {
		t.Fatalf("report missing = %v", report.Missing)
	}

	// An unknown key is the other half of the drift: renamed or typo'd.
	typo := map[string]any{"name": "c2", "judge_ok": true, "judgeok": true, "at": rfc3339(t)}
	if _, err := e.Insert(ctx, "clusters", []map[string]any{typo}); err != nil {
		t.Fatalf("insert typo: %v", err)
	}
	last, _ = e.LastShapeAudit("clusters")
	if !slices.Equal(last.Unknown, []string{"judgeok"}) {
		t.Fatalf("unknown = %v, want [judgeok]", last.Unknown)
	}
	if len(last.Missing) != 0 {
		t.Fatalf("missing = %v, want none", last.Missing)
	}

	// _id/_key are the engine's bookkeeping, never reported as unknown.
	withID := map[string]any{"_id": "mine", "name": "c3", "judge_ok": true, "at": rfc3339(t)}
	if _, err := e.Insert(ctx, "clusters", []map[string]any{withID}); err != nil {
		t.Fatalf("insert with id: %v", err)
	}
	if last, _ = e.LastShapeAudit("clusters"); last.violated() {
		t.Fatalf("identity keys flagged: %v", last)
	}
}

func TestShapeStrictFailsTheWrite(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()
	mustEnsure(t, e, "clusters")
	mustSetShape(t, e, "clusters", clusterDoc{})

	// A document that predates strict: shape declared, strict off, the
	// missing required field is remembered but the write lands.
	predates := map[string]any{"name": "predates", "at": rfc3339(t)}
	idsOld := mustInsert(t, e, "clusters", predates)
	if err := e.SetShapeStrict(ctx, "clusters", true); err != nil {
		t.Fatalf("set strict: %v", err)
	}

	// SetShapeStrict cannot arm an undeclared shape.
	if err := e.SetShapeStrict(ctx, "other", true); !IsNotFound(err) {
		t.Fatalf("strict on unshaped coll err = %v, want not-found", err)
	}

	// Strict mode fails the drifted write and stores nothing.
	if _, err := e.Insert(ctx, "clusters", []map[string]any{map[string]any{"name": "lost"}}); !errors.Is(err, ErrShapeViolation) {
		t.Fatalf("strict insert err = %v, want ErrShapeViolation", err)
	}
	page, err := e.Query(ctx, "clusters", contract.Query{Filter: map[string]any{"name": "lost"}})
	if err != nil || page.Count != 0 {
		t.Fatalf("strict failure stored a document: count=%d err=%v", page.Count, err)
	}

	// An unknown key fails strict too.
	typo := map[string]any{"name": "c", "judge_ok": true, "naem": "typo", "at": rfc3339(t)}
	if _, err := e.Insert(ctx, "clusters", []map[string]any{typo}); !errors.Is(err, ErrShapeViolation) {
		t.Fatalf("strict typo err = %v, want ErrShapeViolation", err)
	}

	// A well-formed struct write sails through, pointer field included.
	retired := false
	if _, err := e.InsertStructs(ctx, "clusters", []any{
		clusterDoc{Name: "good", JudgeOK: true, Retired: &retired, At: time.Now().UTC()}}); err != nil {
		t.Fatalf("strict good insert: %v", err)
	}

	// The patch on the predating document is refused: the post-patch state
	// would still miss judge_ok, and strict mode does not launder it.
	if _, err := e.PatchDocument(ctx, "clusters", idsOld[0],
		map[string]any{"$set": map[string]any{"note": "x"}}); !errors.Is(err, ErrShapeViolation) {
		t.Fatalf("patch err = %v, want ErrShapeViolation", err)
	}

	// Re-declaring the shape (ensure-style startup) keeps strictness armed.
	mustSetShape(t, e, "clusters", clusterDoc{})
	if _, err := e.Insert(ctx, "clusters", []map[string]any{{"name": "x", "at": rfc3339(t)}}); !errors.Is(err, ErrShapeViolation) {
		t.Fatalf("redeclared shape lost strictness: %v", err)
	}

	// Disarm: the same write lands and is remembered instead.
	if err := e.SetShapeStrict(ctx, "clusters", false); err != nil {
		t.Fatalf("unset strict: %v", err)
	}
	ids2, err := e.Insert(ctx, "clusters", []map[string]any{{"name": "x", "at": rfc3339(t)}})
	if err != nil {
		t.Fatalf("lenient insert: %v", err)
	}
	last, ok := e.LastShapeAudit("clusters")
	if !ok || !slices.Equal(last.Missing, []string{"judge_ok"}) || last.DocumentID != ids2[0] {
		t.Fatalf("lenient audit = %+v (found %v)", last, ok)
	}
}

func TestShapeOmittedEmptyIsLegal(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()
	mustEnsure(t, e, "c")
	mustSetShape(t, e, "c", clusterDoc{})

	// An omitempty field at zero is lawfully absent: no violation, but the
	// report shows what this document chose not to carry.
	doc := map[string]any{"name": "c1", "judge_ok": true, "at": rfc3339(t)}
	report, err := e.ShapeReport("c", doc)
	if err != nil {
		t.Fatalf("shape report: %v", err)
	}
	if report.violated() {
		t.Fatalf("zero omitempty must not violate: %+v", report)
	}
	if !slices.Equal(report.Absent, []string{"note", "retired"}) {
		t.Fatalf("absent = %v, want [note retired]", report.Absent)
	}

	if _, err := e.Insert(ctx, "c", []map[string]any{doc}); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestUnshapedCollectionBehavesAsBefore(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()
	mustEnsure(t, e, "c")

	// No shape declared: every behaviour is the pre-shape engine's.
	if _, ok := e.LastShapeAudit("c"); ok {
		t.Fatal("no audit can exist for an unshaped collection")
	}
	if _, err := e.ShapeReport("c", map[string]any{"any": "key"}); !IsNotFound(err) {
		t.Fatalf("shape report err = %v, want not-found", err)
	}
	ids := mustInsert(t, e, "c", map[string]any{"name": "x", "anything": 1.0})
	if _, err := e.GetDocument(ctx, "c", ids[0]); err != nil {
		t.Fatalf("get: %v", err)
	}
}

func TestShapeEmbeddedStructInlined(t *testing.T) {
	type base struct {
		Source string `json:"source"`
		Skip   string `json:"-"`
	}
	type withEmbed struct {
		base
		Kind  string  `json:"kind"`
		Extra *string `json:"extra,omitempty"`
	}
	e := openEngine(t)
	ctx := context.Background()
	mustEnsure(t, e, "c")
	mustSetShape(t, e, "c", withEmbed{})
	if err := e.SetShapeStrict(ctx, "c", true); err != nil {
		t.Fatalf("set strict: %v", err)
	}

	// The embedded tags are top-level document keys, exactly as encoding/json
	// spells the struct: the zero struct writes "source":"" and satisfies the
	// shape; a hand-built map that drops the key violates it; the `-` tag and
	// the nil pointer are not required.
	if _, err := e.InsertStructs(ctx, "c", []any{withEmbed{Kind: "k"}}); err != nil {
		t.Fatalf("zero embedded field marshals and satisfies: %v", err)
	}
	if _, err := e.Insert(ctx, "c", []map[string]any{{"kind": "k"}}); !errors.Is(err, ErrShapeViolation) {
		t.Fatalf("hand map without embedded source err = %v, want ErrShapeViolation", err)
	}
	val := withEmbed{base: base{Source: "s"}, Kind: "k"}
	ids, err := e.InsertStructs(ctx, "c", []any{val})
	if err != nil {
		t.Fatalf("insert embed: %v", err)
	}
	doc, err := e.GetDocument(ctx, "c", ids[0])
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if doc["source"] != "s" || doc["kind"] != "k" || doc["skip"] != nil {
		t.Fatalf("doc = %v", doc)
	}
	if diff, err := e.VerifyDoc(ctx, "c", ids[0], val); err != nil || len(diff) != 0 {
		t.Fatalf("honest round-trip diff = %v (err %v)", diff, err)
	}
}

func TestVerifyDocCatchesJudgeOkDrift(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()
	mustEnsure(t, e, "clusters")

	// The incident, one call wide: the hand-built table dropped judge_ok,
	// the write landed, reads looked whole. VerifyDoc is the proof it was
	// not — against the original itself, no shape needed.
	at := time.Now().UTC()
	hand := map[string]any{"name": "c1", "at": at.Format(time.RFC3339Nano)}
	ids := mustInsert(t, e, "clusters", hand)

	diff, err := e.VerifyDoc(ctx, "clusters", ids[0], clusterDoc{Name: "c1", JudgeOK: true, At: at})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !slices.Contains(diff, "lost: judge_ok") {
		t.Fatalf("diff = %v, want lost: judge_ok", diff)
	}

	// The honest path verifies clean, identity bookkeeping excluded.
	ids2, err := e.InsertStructs(ctx, "clusters", []any{clusterDoc{Name: "c2", JudgeOK: true, At: at}})
	if err != nil {
		t.Fatalf("insert structs: %v", err)
	}
	if diff, err := e.VerifyDoc(ctx, "clusters", ids2[0], clusterDoc{Name: "c2", JudgeOK: true, At: at}); err != nil || len(diff) != 0 {
		t.Fatalf("honest round-trip diff = %v (err %v)", diff, err)
	}

	// Gained and changed keys round out the diff.
	extra := map[string]any{"name": "c3", "status": "stale", "at": at.Format(time.RFC3339Nano)}
	ids3 := mustInsert(t, e, "clusters", extra)
	diff, err = e.VerifyDoc(ctx, "clusters", ids3[0], clusterDoc{Name: "other", JudgeOK: true, At: at})
	if err != nil {
		t.Fatalf("verify 3: %v", err)
	}
	for _, want := range []string{"lost: judge_ok", "changed: name", "gained: status"} {
		found := false
		for _, line := range diff {
			if strings.HasPrefix(line, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("diff = %v, want %q", diff, want)
		}
	}

	// A missing document is a not-found error, not an empty diff.
	if _, err := e.VerifyDoc(ctx, "clusters", "absent", clusterDoc{Name: "x", At: at}); !IsNotFound(err) {
		t.Fatalf("verify absent err = %v, want not-found", err)
	}
}
