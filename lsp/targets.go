package lsp

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func validateTarget(t Target) error {
	valid := false
	switch {
	case t.Ref != "":
		valid = t.Path == "" && t.Line == 0 && t.Column == 0 && t.Symbol == "" && t.Context == nil
	case t.Symbol != "":
		valid = t.Path != "" && t.Line > 0 && t.Column == 0
	default:
		valid = t.Path != "" && t.Line > 0 && t.Column > 0 && t.Context == nil
	}
	if !valid {
		return failure("invalid_query", "provide exactly one target: a reference, path/line/symbol, or a library path/line/column position")
	}
	return nil
}

// Resolve only an exact identifier in the synchronized document. Never guess a
// nearby line, choose the first duplicate, or reinterpret a changed reference.
// The host owns character counting and the subsequent wire-encoding conversion.
func symbolPosition(text, language string, line int, symbol string, context *string) (Position, error) {
	identifierRune := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r) || r == '_' || (r == '$' && language != "shellscript")
	}
	if len(symbol) > 256 || symbol == "" || !utf8.ValidString(symbol) {
		return Position{}, failure("invalid_target", "symbol must be a nonempty identifier of at most 256 bytes")
	}
	for index, r := range symbol {
		if !identifierRune(r) || (index == 0 && unicode.IsDigit(r)) {
			return Position{}, failure("invalid_target", "symbol must be the exact unqualified identifier, such as RateFor; put surrounding code in context")
		}
	}
	ls := lines(text)
	if line < 1 || line > len(ls) {
		return Position{}, failure("target_not_found", "line %d is outside the document; read the current file or rediscover a reference", line)
	}
	source := strings.TrimSuffix(ls[line-1], "\r")
	// A context must itself be unique. This makes a repeated identifier on one
	// line addressable without another model-computed numeric index.
	start, end := 0, len(source)
	if context != nil {
		if *context == "" || len(*context) > 2048 || strings.ContainsAny(*context, "\r\n") {
			return Position{}, failure("invalid_target", "context must be a nonempty single-line source fragment of at most 2048 bytes, or null")
		}
		start = strings.Index(source, *context)
		if start < 0 {
			return Position{}, failure("target_not_found", "context does not match line %d; copy a verbatim source fragment", line)
		}
		if strings.Contains(source[start+1:], *context) {
			return Position{}, failure("ambiguous_target", "context occurs more than once on line %d; provide a longer unique fragment", line)
		}
		end = start + len(*context)
	}
	match := -1
	for offset := start; offset+len(symbol) <= end; {
		index := strings.Index(source[offset:end], symbol)
		if index < 0 {
			break
		}
		index += offset
		next := index + len(symbol)
		offset = next
		if index > 0 {
			previous, _ := utf8.DecodeLastRuneInString(source[:index])
			if identifierRune(previous) {
				continue
			}
		}
		if next < len(source) {
			after, _ := utf8.DecodeRuneInString(source[next:])
			if identifierRune(after) {
				continue
			}
		}
		if match >= 0 {
			return Position{}, failure("ambiguous_target", "%s occurs more than once on line %d; supply context containing only the desired occurrence", symbol, line)
		}
		match = index
	}
	if match < 0 {
		return Position{}, failure("target_not_found", "identifier %s was not found on line %d in the selected context; read the current file or use a returned reference", symbol, line)
	}
	return Position{Line: line, Column: utf8.RuneCountInString(source[:match]) + 1}, nil
}
