package contract

// Schema is a JSON Schema (draft 2020-12) object. Each named contract type
// returns its own from JSONSchema(), built from the same patterns, lists and
// bounds as its Validate() (D14).
type Schema map[string]any
