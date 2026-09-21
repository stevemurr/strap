package lsp

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type wirePosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
type wireRange struct {
	Start wirePosition `json:"start"`
	End   wirePosition `json:"end"`
}
type wireLocation struct {
	URI                  string    `json:"uri"`
	Range                wireRange `json:"range"`
	TargetURI            string    `json:"targetUri"`
	TargetRange          wireRange `json:"targetRange"`
	TargetSelectionRange wireRange `json:"targetSelectionRange"`
}
type wireSymbol struct {
	Name           string        `json:"name"`
	Detail         string        `json:"detail"`
	Kind           int           `json:"kind"`
	Container      string        `json:"containerName"`
	Range          *wireRange    `json:"range"`
	SelectionRange *wireRange    `json:"selectionRange"`
	Location       *wireLocation `json:"location"`
	Children       []wireSymbol  `json:"children"`
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}
func uriPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
		return "", failure("unsupported", "non-local URI %q", uri)
	}
	return filepath.FromSlash(u.Path), nil
}
func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func readBounded(path string, limit int) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, failure("unsupported", "path is not a regular file")
	}
	if info.Size() > int64(limit) {
		return nil, failure("file_too_large", "file exceeds %d bytes", limit)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, failure("file_too_large", "file exceeds %d bytes", limit)
	}
	return b, nil
}
func textFile(path string, limit int) (string, error) {
	b, err := readBounded(path, limit)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", failure("unsupported", "file is not UTF-8")
	}
	return string(b), nil
}
func lines(text string) []string { return strings.Split(text, "\n") }
func width(r rune, encoding string) int {
	switch encoding {
	case "utf-8":
		return utf8.RuneLen(r)
	case "utf-16":
		if r > 0xffff {
			return 2
		}
	}
	return 1
}
func toWire(text string, p Position, encoding string) (wirePosition, error) {
	ls := lines(text)
	if p.Line < 1 || p.Line > len(ls) || p.Column < 1 {
		return wirePosition{}, failure("invalid_position", "line/column out of range")
	}
	line := strings.TrimSuffix(ls[p.Line-1], "\r")
	count, units := 1, 0
	for _, r := range line {
		if count == p.Column {
			return wirePosition{p.Line - 1, units}, nil
		}
		count++
		units += width(r, encoding)
	}
	if count == p.Column {
		return wirePosition{p.Line - 1, units}, nil
	}
	return wirePosition{}, failure("invalid_position", "column beyond line")
}
func fromWire(text string, p wirePosition, encoding string) (Position, error) {
	ls := lines(text)
	if p.Line < 0 || p.Line >= len(ls) || p.Character < 0 {
		return Position{}, failure("invalid_position", "server returned an invalid position")
	}
	count, units := 1, 0
	for _, r := range strings.TrimSuffix(ls[p.Line], "\r") {
		if units == p.Character {
			return Position{p.Line + 1, count}, nil
		}
		units += width(r, encoding)
		count++
		if units > p.Character {
			return Position{}, failure("invalid_position", "server split a Unicode character")
		}
	}
	if units == p.Character {
		return Position{p.Line + 1, count}, nil
	}
	return Position{}, failure("invalid_position", "server returned a column beyond the line")
}
func fromRange(text string, r wireRange, encoding string) (*Range, error) {
	a, err := fromWire(text, r.Start, encoding)
	if err != nil {
		return nil, err
	}
	b, err := fromWire(text, r.End, encoding)
	if err != nil {
		return nil, err
	}
	if b.Line < a.Line || (b.Line == a.Line && b.Column < a.Column) {
		return nil, failure("invalid_position", "reversed server range")
	}
	return &Range{a, b}, nil
}
func endPosition(text, encoding string) wirePosition {
	ls := lines(text)
	p, _ := toWire(text, Position{len(ls), utf8.RuneCountInString(strings.TrimSuffix(ls[len(ls)-1], "\r")) + 1}, encoding)
	return p
}
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
func excerpt(text string, start, end, maxBytes int) string {
	ls := lines(text)
	start = max(1, start)
	end = min(end, len(ls))
	var b strings.Builder
	for i := start; i <= end; i++ {
		v := fmt.Sprintf("%d\t%s\n", i, ls[i-1])
		if b.Len()+len(v) > maxBytes {
			b.WriteString(cut(v, maxBytes-b.Len()))
			break
		}
		b.WriteString(v)
	}
	return b.String()
}
func symbolKind(n int) string {
	k := []string{"unknown", "file", "module", "namespace", "package", "class", "method", "property", "field", "constructor", "enum", "interface", "function", "variable", "constant", "string", "number", "boolean", "array", "object", "key", "null", "enum_member", "struct", "event", "operator", "type_parameter"}
	if n < 1 || n >= len(k) {
		return "unknown"
	}
	return k[n]
}
