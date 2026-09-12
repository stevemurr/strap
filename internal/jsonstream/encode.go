// Package jsonstream encodes the repository's wire DTOs with bounded string and
// binary buffers. Unlike encoding/json.Encoder, it does not stage the full value.
package jsonstream

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func Encode(w io.Writer, v any) error { return value(w, reflect.ValueOf(v), 0) }
func raw(w io.Writer, s string) error { _, err := io.WriteString(w, s); return err }
func quoted(w io.Writer, s string) error {
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8 in record text")
	}
	if err := raw(w, `"`); err != nil {
		return err
	}
	for len(s) > 0 {
		n := min(len(s), 1024)
		for n < len(s) && !utf8.RuneStart(s[n]) {
			n--
		}
		b, err := json.Marshal(s[:n])
		if err != nil {
			return err
		}
		if _, err = w.Write(b[1 : len(b)-1]); err != nil {
			return err
		}
		s = s[n:]
	}
	return raw(w, `"`)
}
func value(w io.Writer, v reflect.Value, depth int) error {
	if depth > 128 {
		return errors.New("record nesting exceeds limit")
	}
	if !v.IsValid() {
		return raw(w, "null")
	}
	if v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return raw(w, "null")
		}
		return value(w, v.Elem(), depth+1)
	}
	if v.Type() == reflect.TypeFor[time.Time]() {
		b, e := json.Marshal(v.Interface())
		if e != nil {
			return e
		}
		_, e = w.Write(b)
		return e
	}
	if v.Type() == reflect.TypeFor[json.RawMessage]() {
		b := v.Bytes()
		if b == nil {
			return raw(w, "null")
		}
		if !json.Valid(b) {
			return errors.New("invalid raw JSON")
		}
		_, e := w.Write(b)
		return e
	}
	switch v.Kind() {
	case reflect.String:
		return quoted(w, v.String())
	case reflect.Bool:
		return raw(w, strconv.FormatBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return raw(w, strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return raw(w, strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		b, e := json.Marshal(v.Interface())
		if e != nil {
			return e
		}
		_, e = w.Write(b)
		return e
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return raw(w, "null")
		}
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			if e := raw(w, `"`); e != nil {
				return e
			}
			enc := base64.NewEncoder(base64.StdEncoding, w)
			_, e := enc.Write(v.Bytes())
			if e != nil {
				return e
			}
			if e = enc.Close(); e != nil {
				return e
			}
			return raw(w, `"`)
		}
		if e := raw(w, "["); e != nil {
			return e
		}
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				if e := raw(w, ","); e != nil {
					return e
				}
			}
			if e := value(w, v.Index(i), depth+1); e != nil {
				return e
			}
		}
		return raw(w, "]")
	case reflect.Struct:
		if e := raw(w, "{"); e != nil {
			return e
		}
		first := true
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			name := tag[0]
			if name == "" {
				name = f.Name
			}
			x := v.Field(i)
			omit := false
			for _, opt := range tag[1:] {
				if opt == "omitempty" {
					switch x.Kind() {
					case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
						omit = x.Len() == 0
					default:
						omit = x.IsZero()
					}
				}
			}
			if omit {
				continue
			}
			if f.Anonymous && tag[0] == "" {
				return fmt.Errorf("embedded DTO field %s requires an explicit JSON name", f.Name)
			}
			if !first {
				if e := raw(w, ","); e != nil {
					return e
				}
			}
			first = false
			if e := quoted(w, name); e != nil {
				return e
			}
			if e := raw(w, ":"); e != nil {
				return e
			}
			if e := value(w, x, depth+1); e != nil {
				return e
			}
		}
		return raw(w, "}")
	case reflect.Map:
		if v.IsNil() {
			return raw(w, "null")
		}
		if v.Type().Key().Kind() != reflect.String {
			return errors.New("record maps require string keys")
		}
		iter := v.MapRange()
		if e := raw(w, "{"); e != nil {
			return e
		}
		first := true
		for iter.Next() {
			key := iter.Key()
			if !first {
				if e := raw(w, ","); e != nil {
					return e
				}
			}
			first = false
			if e := quoted(w, key.String()); e != nil {
				return e
			}
			if e := raw(w, ":"); e != nil {
				return e
			}
			if e := value(w, iter.Value(), depth+1); e != nil {
				return e
			}
		}
		return raw(w, "}")
	}
	return fmt.Errorf("unsupported record value %s", v.Type())
}
