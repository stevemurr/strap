package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/stevemurr/strap/provider"
)

type createWidgetArgs struct {
	Title string   `json:"title"`
	Parts []string `json:"parts"`
}
type editWidgetArgs struct {
	WidgetID         string   `json:"widget_id"`
	ExpectedRevision int      `json:"expected_revision"`
	Title            *string  `json:"title"`
	Parts            []string `json:"parts"`
}

func widgetTool(t *testing.T) Tool {
	t.Helper()
	create := builtin("create_widget", "Create a widget with a title. Omit all IDs.", func(context.Context, Call, createWidgetArgs) (Result, error) { return Text("created"), nil }, MinLength("title", 1), Nullable("parts", "no parts"))
	edit := builtin("edit_widget", "Edit a widget using widget_id and expected_revision.", func(context.Context, Call, editWidgetArgs) (Result, error) { return Text("edited"), nil }, MinLength("widget_id", 1), Minimum("expected_revision", 1), Nullable("title", "unchanged"), Nullable("parts", "unchanged"))
	op, err := Compose(provider.ToolDefinition{Name: "widget"}, create, edit)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// A rejection names each form by its description so the model can tell which
// operation its arguments were closest to, and lists every offending field.
func TestComposedRejectionNamesEachForm(t *testing.T) {
	_, err := widgetTool(t).Call(context.Background(), Call{Arguments: []byte(`{"input":{"title":"w","widget_id":"x","status":"done"}}`)})
	if err == nil {
		t.Fatal("accepted mixed forms")
	}
	for _, want := range []string{
		"widget arguments match no operation: ",
		"form 1, Create a widget with a title: arguments.input.status is not an allowed field (also not allowed: widget_id)",
		"form 2, Edit a widget using widget_id and expected_revision: arguments.input.status is not an allowed field",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rejection %q lacks %q", err, want)
		}
	}
}
