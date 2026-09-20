package tool

import (
	"encoding/json"
	"testing"
)

type nullArgs struct {
	Title string   `json:"title"`
	Note  *string  `json:"note"`
	Tags  []string `json:"tags"`
	Meta  *struct {
		Owner *string `json:"owner"`
	} `json:"meta"`
}

func TestExplicitNullAndOmissionContract(t *testing.T) {
	p, err := NewParameters[nullArgs](Nullable("note", "no note"), Nullable("tags", "no tags"), Nullable("meta", "no metadata"), Nullable("meta.owner", "no owner"))
	if err != nil {
		t.Fatal(err)
	}
	schema := compileExportedSchema(t, p.Schema())
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{"input":{"title":"t","note":null,"tags":null,"meta":null}}`, true},
		{`{"input":{"title":"t","note":"null","tags":[],"meta":{"owner":null}}}`, true},
		{`{"input":{"title":"t","note":"  text\n","tags":["a"],"meta":{"owner":""}}}`, true},
		{`null`, false}, {`{"input":{"title":"t"}}`, false},
		{`{"input":{"title":null,"note":null,"tags":null,"meta":null}}`, false},
		{`{"input":{"title":"t","note":null,"tags":[null],"meta":null}}`, false},
		{`{"input":{"title":"t","note":null,"tags":null,"meta":{}}}`, false},
		{`{"input":{"title":"t","note":null,"tags":null,"meta":null,"extra":null}}`, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			a, de := p.Decode(json.RawMessage(tc.raw))
			se := validateExportedSchema(t, schema, json.RawMessage(tc.raw))
			if (de == nil) != tc.valid || (se == nil) != tc.valid {
				t.Fatalf("valid=%v decoder=%v schema=%v", tc.valid, de, se)
			}
			if tc.valid && a.Note != nil && *a.Note == "null" && a.Tags == nil {
				t.Fatal("empty collection collapsed to null")
			}
		})
	}
	// Every omission, including one of the nullable members, is invalid.
	seed := `{"input":{"title":"t","note":null,"tags":null,"meta":null}}`
	for _, name := range []string{"title", "note", "tags", "meta"} {
		raw := mutateSchemaSeed(t, seed, schemaMutation{path: name})
		if _, err := p.Decode(raw); err == nil {
			t.Fatal("accepted missing", name)
		}
		if err := validateExportedSchema(t, schema, raw); err == nil {
			t.Fatal("schema accepted missing", name)
		}
	}
}

func TestNullableDeclarationIsExplicit(t *testing.T) {
	type legacy struct {
		Value *string `json:"value,omitempty"`
	}
	if _, err := NewParameters[legacy](); err == nil {
		t.Fatal("accepted legacy omission tag")
	}
	type scalar struct {
		Value string `json:"value"`
	}
	if _, err := NewParameters[scalar](Nullable("value", "default")); err == nil {
		t.Fatal("nullable scalar cannot preserve null")
	}
	type pointer struct {
		ID *string `json:"id"`
	}
	p, err := NewParameters[pointer]()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Decode([]byte(`{"input":{"id":null}}`)); err == nil {
		t.Fatal("pointer implicitly nullable")
	}
}

func TestNullableMeaningSurvivesDescriptionOrder(t *testing.T) {
	type input struct {
		Value *string `json:"value"`
	}
	for _, constraints := range [][]Constraint{
		{Nullable("value", "use the default"), Description("value", "Setting.")},
		{Description("value", "Setting."), Nullable("value", "use the default")},
	} {
		p, err := NewParameters[input](constraints...)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct{ Description string }
		}
		if err := json.Unmarshal(inputSchema(t, p.Schema()), &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Properties["value"].Description != "Setting. Null: use the default" {
			t.Fatal(string(p.Schema()))
		}
	}
}
