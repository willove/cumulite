// Package contract holds the wire contract cumulite's Port speaks: the request
// and response shapes of every Port method, plus the not-found idiom.
//
// It is a fork of github.com/willove/cumudb/pkg/client's contract types, made
// field-for-field (names, types, JSON tags) so that a consumer can pass either
// engine's answer to the same decoding code. The fork exists for one reason:
// cumulite must not depend on the cumudb project — not as a build-time require,
// not through a replace directive, not as a sibling checkout. Forking the types
// instead of importing them moves contract drift from a compile-time failure
// (which a sibling checkout would give) to a test-visible failure (the golden
// JSON in contract_test.go pins every field), and the shapes are stable enough
// that drift is a deliberate act, not an accident.
//
// A consumer that legitimately speaks to both engines — e.g. ask, whose default
// path is the HTTP client — keeps both packages' types distinct, as Go demands,
// and converts at the boundary.
//
// # Stance: the document currency is map[string]any, and that is not an
// invitation to translate by hand
//
// Documents cross this contract as map[string]any by design — indexes, KNN
// filters and patches all speak it. That currency is the engine's business;
// it is not a licence for a typed consumer to hand-maintain its own
// struct→map translation table. A hand-built table that forgets a new struct
// field fails SILENTLY: the write stores the smaller map without complaint and
// reads unmarshal the whole document, so the loss surfaces only when someone
// reads the missing field back. The embedded engine therefore ships three
// exits (cumulite.StructPort, cumulite.ShapePort, cumulite.DocVerifier):
// write structs directly so the json tags are the single source of truth, or
// declare a collection shape and let every write be audited against it, or
// verify the round-trip after writing. A typed consumer must use one of the
// three; a second hand-maintained table is the bug that already happened.
package contract
