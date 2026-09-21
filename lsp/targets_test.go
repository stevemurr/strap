package lsp

import (
	"errors"
	"testing"
)

func TestSymbolTargets(t *testing.T) {
	ptr := func(s string) *string { return &s }
	for _, tc := range []struct {
		name, text, language, symbol string
		line, column                 int
		context                      *string
		failure                      string
	}{
		{"declaration", "func RateFor() {}", "go", "RateFor", 1, 6, nil, ""},
		{"tab_qualified_call", "\treturn pricing.RateFor(\"vip\")", "go", "RateFor", 1, 17, nil, ""},
		{"emoji_before_call", "\t_ = \"🌎\"; RateFor()", "go", "RateFor", 1, 11, nil, ""},
		{"crlf_unicode_name", "package p\r\nfunc 価格() {}\r\n", "go", "価格", 2, 6, nil, ""},
		{"identifier_boundary", "xRateFor + RateFor()", "go", "RateFor", 1, 12, nil, ""},
		{"repeated_requires_context", "RateFor(x) + RateFor(y)", "go", "RateFor", 1, 0, nil, "ambiguous_target"},
		{"second_occurrence", "RateFor(x) + RateFor(y)", "go", "RateFor", 1, 14, ptr("RateFor(y)"), ""},
		{"duplicate_context", "RateFor(x) + RateFor(x)", "go", "RateFor", 1, 0, ptr("RateFor(x)"), "ambiguous_target"},
		{"quoted_duplicate", "RateFor(\"RateFor\")", "go", "RateFor", 1, 0, nil, "ambiguous_target"},
		{"call_context_excludes_quote", "RateFor(\"RateFor\")", "go", "RateFor", 1, 1, ptr("RateFor("), ""},
		{"context_does_not_override_boundary", "xRateFor()", "go", "RateFor", 1, 0, ptr("RateFor"), "target_not_found"},
		{"missing_identifier", "func Alpha() {}", "go", "Beta", 1, 0, nil, "target_not_found"},
		{"wrong_line", "func Alpha() {}\n", "go", "Alpha", 2, 0, nil, "target_not_found"},
		{"outside_file", "func Alpha() {}", "go", "Alpha", 2, 0, nil, "target_not_found"},
		{"empty_context", "Alpha()", "go", "Alpha", 1, 0, ptr(""), "invalid_target"},
		{"multiline_context", "Alpha()", "go", "Alpha", 1, 0, ptr("Alpha()\n"), "invalid_target"},
		{"missing_context", "Alpha()", "go", "Alpha", 1, 0, ptr("Beta()"), "target_not_found"},
		{"qualified_is_not_identifier", "pricing.RateFor()", "go", "pricing.RateFor", 1, 0, nil, "invalid_target"},
		{"numeric_is_not_identifier", "return 42", "go", "42", 1, 0, nil, "invalid_target"},
		{"javascript_dollar", "$price + price", "javascript", "price", 1, 10, nil, ""},
		{"javascript_dollar_name", "$price()", "javascript", "$price", 1, 1, nil, ""},
		{"bash_sigil", "echo $price", "shellscript", "price", 1, 7, nil, ""},
		{"bash_braced", "${PATH}", "shellscript", "PATH", 1, 3, nil, ""},
		{"combining_identifier_boundary", "é + e", "python", "e", 1, 6, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			position, err := symbolPosition(tc.text, tc.language, tc.line, tc.symbol, tc.context)
			if tc.failure != "" {
				var issue *Error
				if !errors.As(err, &issue) || issue.Kind != tc.failure {
					t.Fatalf("position=%v err=%v, want %s", position, err, tc.failure)
				}
				return
			}
			if err != nil || position != (Position{tc.line, tc.column}) {
				t.Fatal(position, err)
			}
			for _, encoding := range []string{"utf-8", "utf-16", "utf-32"} {
				wire, err := toWire(tc.text, position, encoding)
				if err != nil {
					t.Fatal(err)
				}
				got, err := fromWire(tc.text, wire, encoding)
				if err != nil || got != position {
					t.Fatal(encoding, got, err)
				}
			}
		})
	}
}

func TestTargetAlternatives(t *testing.T) {
	context := "Alpha()"
	for _, target := range []Target{
		{Ref: "loc_1", Symbol: "Alpha"}, {Ref: "loc_1", Context: &context},
		{Path: "a.go", Line: 1, Column: 2, Symbol: "Alpha"},
		{Path: "a.go", Line: 1, Column: 2, Context: &context},
		{Path: "a.go", Line: 1, Symbol: "Alpha", ExpectedText: &context},
	} {
		if err := validateTarget(target); err == nil {
			t.Fatalf("accepted mixed target: %+v", target)
		}
	}
}
