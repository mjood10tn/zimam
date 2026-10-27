package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

var keyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// DecodeStrict decodes exactly one JSON object into v. It rejects unknown
// fields and trailing data. It also rejects duplicate keys and any key that
// isn't lowercase snake_case: encoding/json matches keys case-insensitively
// and keeps the last duplicate, so {"Command":...} or a repeated key could be
// read differently by the panel and the agent (D6).
//
// It rejects invalid UTF-8, which encoding/json would silently replace with
// U+FFFD, and any top-level value other than an object, since decoding null
// into a struct succeeds and leaves it unchanged.
func DecodeStrict(data []byte, v any) error {
	if !utf8.Valid(data) {
		return errors.New("invalid UTF-8")
	}
	if !bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte("{")) {
		return errors.New("must be a JSON object")
	}
	if err := checkKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

func checkKeys(data []byte) error {
	type frame struct {
		object    bool
		expectKey bool
		seen      map[string]bool
	}
	var stack []*frame
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].expectKey = true
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				stack = append(stack, &frame{object: true, expectKey: true, seen: map[string]bool{}})
			case '[':
				stack = append(stack, &frame{})
			default: // '}' or ']'
				stack = stack[:len(stack)-1]
				valueDone()
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].expectKey {
				top := stack[n-1]
				if !keyRe.MatchString(t) {
					return fmt.Errorf("key %q is not lowercase snake_case", t)
				}
				if top.seen[t] {
					return fmt.Errorf("duplicate key %q", t)
				}
				top.seen[t] = true
				top.expectKey = false
				continue
			}
			valueDone()
		default:
			valueDone()
		}
	}
}
