package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"zimam/agent/contract"
)

var funcs = template.FuncMap{
	"safe":              safe,
	"rcloneValue":       rcloneValue,
	"hostKeyAlgorithms": hostKeyAlgorithms,
	"prefix":            prefix,
	"htpasswdLine":      htpasswdLine,
	"laravelKey":        laravelKey,
	"sha256hex":         sha256hex,
	"shquote":           shquote,
	"cronSchedule":      cronSchedule,
	"siteName":          siteName,
	"planID":            planID,
	"onCalendar":        onCalendar,
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

// rcloneValueRe is safeRe plus "+" and "=", which S3 secret keys and
// rclone's obscured values hold; rclone.conf splits a line at its first "=",
// so a later one stays in the value.
var rcloneValueRe = regexp.MustCompile(`^[A-Za-z0-9._:/@+=-]+$`)

// rcloneValue renders a string-kind value into rclone.conf (R05-D2) only if
// it matches rcloneValueRe: it can't end the line or start a section.
func rcloneValue(v any) (string, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.String {
		return "", fmt.Errorf("rcloneValue: %T is not a string", v)
	}
	if s := rv.String(); rcloneValueRe.MatchString(s) {
		return s, nil
	}
	return "", fmt.Errorf("rcloneValue: %q may only hold letters, digits and ._:/@+=-", rv.String())
}

// HostKeyAlgorithms are the values an SFTP remote's host_key_algorithms may
// take: the algorithm of its one pinned host key. Without it, a server that
// offers another key type first fails with "key mismatch" (R05-D2, Task 1
// spike).
var HostKeyAlgorithms = []string{
	"ssh-ed25519",
	"ecdsa-sha2-nistp256",
	"ecdsa-sha2-nistp384",
	"ecdsa-sha2-nistp521",
	"rsa-sha2-512 rsa-sha2-256",
}

func hostKeyAlgorithms(s string) (string, error) {
	if slices.Contains(HostKeyAlgorithms, s) {
		return s, nil
	}
	return "", fmt.Errorf("hostKeyAlgorithms: %q is not a known value", s)
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

// laravelKeyRe is a Laravel APP_KEY for AES-256: "base64:" and 32 bytes.
var laravelKeyRe = regexp.MustCompile(`^base64:[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$`)

// laravelKey renders an APP_KEY only in its exact shape. The key is a
// secret, so the error never quotes it.
func laravelKey(k string) (string, error) {
	if !laravelKeyRe.MatchString(k) {
		return "", fmt.Errorf("laravelKey: the key isn't 32 bytes in Laravel's APP_KEY form")
	}
	return k, nil
}

// sha256hex renders a SHA-256 sum as the 64 lowercase hex digits Valkey's ACL
// file takes after "#". A sum is bytes, so it can't carry anything else into
// the file.
func sha256hex(sum [sha256.Size]byte) string { return hex.EncodeToString(sum[:]) }

// shquote single-quotes a word for bash, so whatever the word holds stays one
// argument: the only character a single-quoted string can't hold is ', which
// closes the quotes, adds \' and reopens them. It refuses NUL, which no
// argument can carry, and renders nothing for a non-string.
func shquote(v any) (string, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.String {
		return "", fmt.Errorf("shquote: %T is not a string", v)
	}
	s := rv.String()
	if strings.IndexByte(s, 0) >= 0 {
		return "", fmt.Errorf("shquote: the word contains NUL")
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'", nil
}

// cronSchedule renders a schedule only in its normal form: what
// contract.ParseCronSchedule returns. A stored schedule need not be normal, so
// the caller passes the parsed one; anything else is refused here, so a
// schedule that is not one never becomes a cron line (R04-D5).
func cronSchedule(v any) (string, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.String {
		return "", fmt.Errorf("cronSchedule: %T is not a string", v)
	}
	s := rv.String()
	if n, err := contract.ParseCronSchedule(s); err != nil || string(n) != s {
		return "", fmt.Errorf("cronSchedule: %q isn't a schedule in normal form", s)
	}
	return s, nil
}

// siteName renders a site name only if it is one. A name with a dot would
// make cron ignore /etc/cron.d/zimam-<name> without a word (spike), and one
// that isn't a site's would name another user.
func siteName(v any) (string, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.String {
		return "", fmt.Errorf("siteName: %T is not a string", v)
	}
	if err := contract.SiteName(rv.String()).Validate(); err != nil {
		return "", fmt.Errorf("siteName: %q %v", rv.String(), err)
	}
	return rv.String(), nil
}

// planID renders a backup plan's id only if it is one: it names the units,
// and ExecStart= hands it to the agent.
func planID(v contract.PlanID) (string, error) {
	if err := v.Validate(); err != nil {
		return "", fmt.Errorf("planID: %q %v", v, err)
	}
	return string(v), nil
}

// onCalendar renders a valid backup schedule as OnCalendar= takes it
// (R05-D7); anything else is refused.
func onCalendar(s contract.BackupSchedule) (string, error) {
	if err := s.Validate(); err != nil {
		return "", fmt.Errorf("onCalendar: %q %v", s, err)
	}
	return s.OnCalendar(), nil
}
