package sysexec

import (
	"net/http"
	"testing"
)

func TestDepsHTTPClient(t *testing.T) {
	c := &http.Client{}
	if got := (Deps{HTTP: c}).HTTPClient(); got != c {
		t.Error("HTTPClient doesn't return Deps.HTTP")
	}
	if def := (Deps{}).HTTPClient(); def == nil || def.Timeout <= 0 {
		t.Errorf("the default client has no timeout: %+v", def)
	}
}
