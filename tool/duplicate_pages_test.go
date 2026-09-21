package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDuplicatePagesAreAnApplicationRule(t *testing.T) {
	requirePoppler(t)
	pdf, err := NewPDF(PDFConfig{Dir: "testdata", MaxPages: 2})
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"input":{"path":"pages.pdf","pages":[1,1]}}`)
	args, err := pdf.Spec.Parameters.Decode(raw)
	if err != nil || len(args.Pages) != 2 {
		t.Fatalf("schema rejected or changed list: %v %v", args, err)
	}
	if _, err := pdf.Call(context.Background(), Call{Arguments: raw}); err == nil || !strings.Contains(err.Error(), "duplicate page") {
		t.Fatalf("application rejection missing: %v", err)
	}
}
