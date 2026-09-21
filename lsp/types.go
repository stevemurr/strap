package lsp

import "fmt"

// Positions exposed by Strap are one-based Unicode code point offsets, not tabs'
// display width or the server's negotiated code units. Range ends are exclusive.
type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}
type Target struct {
	Ref, Path    string
	Line, Column int
	ExpectedText *string
	// Symbol selects an exact identifier on Line; Context optionally restricts
	// it to a verbatim single-line fragment. Column/ExpectedText are library-only
	// alternatives for callers which already have an exact position.
	Symbol  string
	Context *string
}
type PageQuery struct {
	Limit  int
	Cursor string
}
type SymbolQuery struct {
	Query, Path string
	PageQuery
}
type OutlineQuery struct {
	Path  string
	Depth int
	PageQuery
}
type InspectQuery struct {
	Target        Target
	IncludeSource bool
}
type NavigateQuery struct {
	Target   Target
	Relation string
	PageQuery
}
type ReferenceQuery struct {
	Target             Target
	IncludeDeclaration bool
	PageQuery
}
type DiagnosticQuery struct {
	Paths []string
	PageQuery
}

type Source struct {
	Server        string `json:"server"`
	Root          string `json:"root"`
	Generation    uint64 `json:"generation"`
	Configuration string `json:"configuration"`
}
type Metadata struct {
	Sources   []Source          `json:"sources"`
	Freshness string            `json:"freshness"`
	Coverage  string            `json:"coverage"`
	Partial   bool              `json:"partial"`
	Issues    []string          `json:"issues,omitempty"`
	Checks    []DiagnosticCheck `json:"checks,omitempty"`
}
type DiagnosticCheck struct {
	Path      string `json:"path"`
	Server    string `json:"server"`
	Freshness string `json:"freshness"`
	Count     int    `json:"count"`
}
type Item struct {
	Ref         string `json:"ref,omitempty"`
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Container   string `json:"container,omitempty"`
	Path        string `json:"path"`
	URI         string `json:"uri,omitempty"`
	Selection   *Range `json:"selection,omitempty"`
	Declaration *Range `json:"declaration,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Excerpt     string `json:"excerpt,omitempty"`
	Depth       int    `json:"depth,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
	Source      string `json:"source,omitempty"`
	Freshness   string `json:"freshness,omitempty"`
}
type Page struct {
	Items      []Item   `json:"items"`
	NextCursor string   `json:"next_cursor,omitempty"`
	Truncated  bool     `json:"truncated"`
	Metadata   Metadata `json:"metadata"`
}
type Inspection struct {
	Location      Item     `json:"location"`
	Documentation string   `json:"documentation"`
	Truncated     bool     `json:"truncated"`
	Metadata      Metadata `json:"metadata"`
}
type ServerStatus struct {
	ID         string   `json:"id"`
	Root       string   `json:"root,omitempty"`
	State      string   `json:"state"`
	Version    string   `json:"version,omitempty"`
	Generation uint64   `json:"generation,omitempty"`
	Operations []string `json:"operations,omitempty"`
	Error      string   `json:"error,omitempty"`
}
type Status struct {
	Servers []ServerStatus `json:"servers"`
}
type Error struct {
	Kind    string
	Message string
}

func (e *Error) Error() string { return e.Kind + ": " + e.Message }
func failure(kind, format string, args ...any) error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}
