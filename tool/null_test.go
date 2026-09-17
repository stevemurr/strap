package tool

import (
	"encoding/json"
	"testing"
)

type nullArgs struct {
	Title string   `json:"title"`
	Note  *string  `json:"note,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	Meta  *struct {
		Owner string `json:"owner,omitempty"`
	} `json:"meta,omitempty"`
}

// Omission and explicit null are different JSON values. A schema advertising a
// string, object, or array must not silently accept null in any of those slots.
func TestNullDoesNotMeanOmitted(t *testing.T) {
	params, err := NewParameters[nullArgs]()
	if err != nil {
		t.Fatal(err)
	}
	schema := compileExportedSchema(t, params.Schema())
	for _, raw := range []string{
		`null`,
		`{"title":null}`,
		`{"title":"t","note":null}`,
		`{"title":"t","tags":null}`,
		`{"title":"t","tags":["a",null,"b"]}`,
		`{"title":"t","meta":null}`,
		`{"title":"t","meta":{"owner":null}}`,
		`{"title":"t","unknown":null}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if err := validateExportedSchema(t, schema, json.RawMessage(raw)); err == nil {
				t.Fatal("schema accepted null", raw)
			}
			if _, err := params.Decode(json.RawMessage(raw)); err == nil {
				t.Fatal("decoder accepted null", raw)
			}
		})
	}
	for _, raw := range []string{`{"title":"t"}`, `{"title":"t","note":"","tags":[],"meta":{}}`} {
		if err := validateExportedSchema(t, schema, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := params.Decode(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
}
