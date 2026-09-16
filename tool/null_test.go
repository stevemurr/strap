package tool

import (
	"encoding/json"
	"strings"
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

// Models send null for fields they mean to leave out. Null is treated as
// omitted everywhere: optional fields and array elements disappear, a
// required field set to null is reported as missing, and a top-level null is
// still not an object.
func TestNullMeansOmitted(t *testing.T) {
	params, err := NewParameters[nullArgs]()
	if err != nil {
		t.Fatal(err)
	}
	got, err := params.Decode(json.RawMessage(`{"title":"t","note":null,"tags":["a",null,"b"],"meta":{"owner":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "t" || got.Note != nil || strings.Join(got.Tags, ",") != "a,b" || got.Meta == nil || got.Meta.Owner != "" {
		t.Fatalf("%+v", got)
	}
	if _, err := params.Decode(json.RawMessage(`{"title":null}`)); err == nil || !strings.Contains(err.Error(), "title is required") {
		t.Fatalf("required null: %v", err)
	}
	if _, err := params.Decode(json.RawMessage(`null`)); err == nil || !strings.Contains(err.Error(), "not null") {
		t.Fatalf("top-level null: %v", err)
	}
}
