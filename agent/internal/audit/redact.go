package audit

import (
	"reflect"
	"slices"
	"strings"
)

// Redact turns params into plain JSON-ready values, with every field tagged
// secret:"true" replaced by "[redacted]", at any depth.
func Redact(params any) any {
	return redactValue(reflect.ValueOf(params))
}

func redactValue(v reflect.Value) any {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Invalid: // Redact(nil)
		return nil
	case reflect.Struct:
		out := map[string]any{}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if slices.Contains(strings.Split(opts, ","), "omitempty") && isEmpty(v.Field(i)) {
				continue
			}
			if name == "" {
				name = f.Name
			}
			if f.Tag.Get("secret") == "true" {
				out[name] = "[redacted]"
				continue
			}
			out[name] = redactValue(v.Field(i))
		}
		return out
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = redactValue(v.Index(i))
		}
		return out
	case reflect.Map:
		// Params types never contain maps (TestParamsHaveNoMaps in Task 8),
		// so a map here is a bug: drop it rather than risk logging a secret.
		return "[redacted]"
	default:
		return v.Interface()
	}
}

// isEmpty is encoding/json's test for omitempty.
func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Interface, reflect.Pointer:
		return v.IsZero()
	}
	return false
}
