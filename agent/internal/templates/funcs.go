package templates

import (
	"fmt"
	"net/netip"
	"reflect"
	"regexp"
	"text/template"

	"zimam/agent/contract"
)

var funcs = template.FuncMap{
	"safe":         safe,
	"prefix":       prefix,
	"htpasswdLine": htpasswdLine,
}

// safeRe is all a string may contain on its way into a config file. Without
// whitespace, quotes, ";", braces, "$" or "#", a value can't end a directive
// or start another one.
var safeRe = regexp.MustCompile(`^[A-Za-z0-9._:/@-]+$`)

// safe renders a string-kind value, such as a contract.Domain, only if it
// matches safeRe. Values are validated before they reach a template; this is
// the last check before a file root reads.
func safe(v any) (string, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.String {
		return "", fmt.Errorf("safe: %T is not a string", v)
	}
	s := rv.String()
	if !safeRe.MatchString(s) {
		return "", fmt.Errorf("safe: %q may only hold letters, digits and ._:/@-", s)
	}
	return s, nil
}

// prefix renders an IP entry as contract.ParseIPEntry returns it: valid,
// masked, without a zone and not IPv4-mapped (D21).
func prefix(p netip.Prefix) (string, error) {
	if !p.IsValid() || p != p.Masked() || p.Addr().Zone() != "" || p.Addr().Is4In6() {
		return "", fmt.Errorf("prefix: %v is not a canonical IP prefix", p)
	}
	return p.String(), nil
}

// htpasswdLine renders one "username:hash" line after validating both again,
// so neither can add a field or a line.
func htpasswdLine(u contract.BasicAuthUser) (string, error) {
	if err := (contract.BasicAuthUsers{u}).Validate(); err != nil {
		return "", fmt.Errorf("htpasswdLine: %v", err)
	}
	return u.Username + ":" + u.PasswordHash, nil
}
