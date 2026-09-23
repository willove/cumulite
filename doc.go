// Package cumulite is the lite CumuDB: an embedded, single-file storage engine
// that speaks the same contract as the CumuDB Go client (pkg/client), covering
// only the modalities a suite like ask actually uses — documents, key/value,
// vector KNN and a change log.
//
// It exists to answer one question with executable evidence: what does a
// consumer really depend on when it says it "runs on CumuDB"? Port is that
// answer, spelled out as an interface — sixteen methods, no query language, no
// full-text search, no graph operators, no time series. A consumer written
// against *client.Client satisfies Port unchanged (Client is a superset), and
// the embedded Engine satisfies it without a server.
//
// The dependency on github.com/willove/cumudb is types-only: pkg/client is a
// pure-standard-library package that defines the wire contract (Query,
// QueryResult, KNNRequest, IndexRequest, ChangeRecord, ...) and the not-found
// sentinel. No server code is imported, so the engine stays small and the
// contract cannot drift between the two implementations.
package cumulite
