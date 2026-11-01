package agriculture

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"time"
	"yingce/backend/internal/agricanonical"
)

// Farm DTOs deliberately include GeoJSON RawMessage and RFC3339 timestamps.
// Keep their exact-key decoder separate from the narrower identity protocol.
func strictFarmJSON(raw []byte, target any, max int) error {
	if len(raw) == 0 || len(raw) > max {
		return ErrInvalid
	}
	if _, e := agricanonical.Canonicalize(raw); e != nil {
		return ErrInvalid
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil || !farmExactKeys(value, reflect.TypeOf(target)) {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func farmExactKeys(value any, t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if value == nil {
		return false
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return true
	}
	if t == reflect.TypeOf(time.Time{}) {
		_, ok := value.(string)
		return ok
	}
	switch t.Kind() {
	case reflect.Interface:
		return true
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok || t.Key().Kind() != reflect.String {
			return false
		}
		for _, v := range object {
			if !farmExactKeys(v, t.Elem()) {
				return false
			}
		}
		return true
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if f.PkgPath != "" || name == "-" {
				continue
			}
			if name == "" || f.Anonymous {
				return false
			}
			fields[name] = f.Type
		}
		for k, v := range object {
			ft, ok := fields[k]
			if !ok || !farmExactKeys(v, ft) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		a, ok := value.([]any)
		if !ok {
			return false
		}
		for _, v := range a {
			if !farmExactKeys(v, t.Elem()) {
				return false
			}
		}
		return true
	default:
		return identityExactJSONKeys(value, t)
	}
}
