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
package contract
