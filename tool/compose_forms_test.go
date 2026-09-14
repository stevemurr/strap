package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stevemurr/strap/provider"
)

type createWidgetArgs struct {
	Title string   `json:"title"`
	Parts []string `json:"parts,omitempty"`
}
type editWidgetArgs struct {
	WidgetID         string   `json:"widget_id"`
	ExpectedRevision int      `json:"expected_revision"`
	Title            *string  `json:"title,omitempty"`
	Parts            []string `json:"parts,omitempty"`
}

func widgetTool(t *testing.T) Tool {
	t.Helper()
	create := builtin("create_widget", "Create a widget with a title. Omit all IDs.", func(context.Context, Call, createWidgetArgs) (Result, error) { return Text("created"), nil }, MinLength("title", 1))
	edit := builtin("edit_widget", "Edit a widget using widget_id and expected_revision.", func(context.Context, Call, editWidgetArgs) (Result, error) { return Text("edited"), nil }, MinLength("widget_id", 1), Minimum("expected_revision", 1))
	op, err := Compose(provider.ToolDefinition{Name: "widget"}, create, edit)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// A rejection names each form by its description so the model can tell which
// operation its arguments were closest to, and lists every offending field.
func TestComposedRejectionNamesEachForm(t *testing.T) {
	_, err := widgetTool(t).Call(context.Background(), Call{Arguments: []byte(`{"title":"w","widget_id":"x","status":"done"}`)})
	if err == nil {
		t.Fatal("accepted mixed forms")
	}
	for _, want := range []string{
		"widget arguments match no operation: ",
		"form 1, Create a widget with a title: arguments.status is not an allowed field (also not allowed: widget_id)",
		"form 2, Edit a widget using widget_id and expected_revision: arguments.status is not an allowed field",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rejection %q lacks %q", err, want)
		}
	}
}

// Servers that flatten oneOf see every property; those owned by a subset of
// forms carry a description naming the accepting forms.
func TestFlattenedHintsAnnotateFormSpecificProperties(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(widgetTool(t).Definition().Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{"widget_id": "Only in the edit form", "expected_revision": "Only in the edit form", "title": "", "parts": ""} {
		if got := schema.Properties[field].Description; got != want {
			t.Fatalf("%s: %q, want %q", field, got, want)
		}
	}
}
