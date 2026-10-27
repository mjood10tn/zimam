package contract

// The JSON Schemas in contract/schema/ are generated from this package's
// command and envelope types (design §6). CI fails when they drift.
//go:generate go run ../tools/schemagen -out ../../contract/schema
