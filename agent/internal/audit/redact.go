package audit

import (
	"reflect"
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
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
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
