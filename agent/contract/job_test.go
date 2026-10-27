package contract

import (
	"encoding/json"
	"testing"
)

// A job's state goes on the wire as "status", as in design §5.2's result line.
func TestJobResultsUseStatus(t *testing.T) {
	for _, v := range []any{JobStatusResult{}, JobCancelResult{}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(b, &keys); err != nil {
			t.Fatal(err)
		}
		if _, ok := keys["status"]; !ok {
			t.Errorf("%T has no status key: %s", v, b)
		}
		if _, ok := keys["state"]; ok {
			t.Errorf("%T has a state key: %s", v, b)
		}
	}
}
