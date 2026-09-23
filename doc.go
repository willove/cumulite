// Package cumulite is the lite CumuDB: an embedded, single-file storage engine
// that speaks the Port contract, covering only the modalities a suite like ask
// actually uses — documents, key/value, vector KNN and a change log.
//
// It exists to answer one question with executable evidence: what does a
// consumer really depend on when it says it "runs on CumuDB"? Port is that
// answer, spelled out as an interface — sixteen methods, no query language, no
// full-text search, no graph operators, no time series. The embedded Engine
// satisfies it without a server.
//
// The contract types live in the contract subpackage, forked field-for-field
// from the CumuDB HTTP client so this repository has no dependency on the
// cumudb project: one require, one backend, no replace directive, no sibling
// checkout. A consumer driving both engines (ask, whose default path is the
// HTTP client) converts types at its own boundary.
package cumulite
