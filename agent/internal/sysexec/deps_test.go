package sysexec

import (
	"context"
	"net"
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

func TestDepsDNSResolver(t *testing.T) {
	var r fakeResolver
	if got := (Deps{DNS: r}).DNSResolver(); got != r {
		t.Error("DNSResolver doesn't return Deps.DNS")
	}
	if got := (Deps{}).DNSResolver(); got != net.DefaultResolver {
		t.Errorf("the default resolver is %v, want net.DefaultResolver", got)
	}
}

type fakeResolver struct{}

func (fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) { return nil, nil }
