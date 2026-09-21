package tool

import (
	"context"
	"errors"

	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/provider"
)

// LanguageServices is the semantic surface consumed by model tools. It is
// implemented once by lsp.Manager, not by each language's setup adapter.
type LanguageServices interface {
	Status(context.Context, string) (lsp.Status, error)
	Symbols(context.Context, lsp.SymbolQuery) (lsp.Page, error)
	Outline(context.Context, lsp.OutlineQuery) (lsp.Page, error)
	Inspect(context.Context, lsp.InspectQuery) (lsp.Inspection, error)
	Navigate(context.Context, lsp.NavigateQuery) (lsp.Page, error)
	References(context.Context, lsp.ReferenceQuery) (lsp.Page, error)
	Diagnostics(context.Context, lsp.DiagnosticQuery) (lsp.Page, error)
}
type LSPPageArgs struct {
	Limit  *int    `json:"limit"`
	Cursor *string `json:"cursor"`
}

func (p LSPPageArgs) query() lsp.PageQuery {
	return lsp.PageQuery{Limit: value(p.Limit, 0), Cursor: value(p.Cursor, "")}
}

type LSPReferenceTarget struct {
	TargetKind string `json:"target_kind"`
	Ref        string `json:"ref"`
}

func (t LSPReferenceTarget) target() lsp.Target { return lsp.Target{Ref: t.Ref} }

type LSPSymbolTarget struct {
	TargetKind string  `json:"target_kind"`
	Path       string  `json:"path"`
	Line       int     `json:"line"`
	Symbol     string  `json:"symbol"`
	Context    *string `json:"context"`
}

func (t LSPSymbolTarget) target() lsp.Target {
	return lsp.Target{Path: t.Path, Line: t.Line, Symbol: t.Symbol, Context: t.Context}
}

type lspStatusArgs struct {
	Path *string `json:"path"`
}
type lspSymbolArgs struct {
	Query string  `json:"query"`
	Path  *string `json:"path"`
	LSPPageArgs
}
type lspOutlineArgs struct {
	Path  string `json:"path"`
	Depth *int   `json:"depth"`
	LSPPageArgs
}
type lspInspectRef struct {
	LSPReferenceTarget
	IncludeSource *bool `json:"include_source"`
}
type lspInspectSymbol struct {
	LSPSymbolTarget
	IncludeSource *bool `json:"include_source"`
}
type lspNavigateRef struct {
	LSPReferenceTarget
	Relation string `json:"relation"`
	LSPPageArgs
}
type lspNavigateSymbol struct {
	LSPSymbolTarget
	Relation string `json:"relation"`
	LSPPageArgs
}
type lspReferencesRef struct {
	LSPReferenceTarget
	IncludeDeclaration *bool `json:"include_declaration"`
	LSPPageArgs
}
type lspReferencesSymbol struct {
	LSPSymbolTarget
	IncludeDeclaration *bool `json:"include_declaration"`
	LSPPageArgs
}
type lspDiagnosticArgs struct {
	Paths []string `json:"paths"`
	LSPPageArgs
}

func value[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}
func languageResult[T any](v T, err error) (Result, error) {
	if err != nil {
		return Result{}, err
	}
	return JSON(v)
}

func LSPTools(service LanguageServices) ([]Tool, error) {
	if service == nil {
		return nil, errors.New("language service is required")
	}
	pageRules := []Constraint{Nullable("limit", "use the default of 50 items"), Minimum("limit", 1), Maximum("limit", 200), Nullable("cursor", "start a new query"), MinLength("cursor", 1)}
	refRules := []Constraint{Enum("target_kind", "reference"), MinLength("ref", 1), Description("ref", "Copy a handle actually supplied by the user or a previous tool result. Never invent a handle. If none is available for this target, use target_kind=symbol or discover it first.")}
	symbolRules := []Constraint{Enum("target_kind", "symbol"), MinLength("path", 1), Description("path", "File containing the source identifier."), Minimum("line", 1), MinLength("symbol", 1), Description("symbol", "Exact unqualified identifier from the source, e.g. RateFor, not pricing.RateFor or an entire declaration"), Description("line", "1-based source line containing the identifier; no character columns are needed"), Nullable("context", "the identifier occurs only once on this line"), MinLength("context", 1), Description("context", "Verbatim single-line source fragment containing only the desired occurrence when the identifier repeats on the line, e.g. RateFor(\"vip\"); otherwise null")}
	rules := func(groups ...[]Constraint) []Constraint {
		var out []Constraint
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}
	tools := []Tool{
		builtin("lsp_status", "Inspect configured language servers, roots, running state and capabilities. Does not launch servers. Null path lists the session.", func(ctx context.Context, _ Call, a lspStatusArgs) (Result, error) {
			return languageResult(service.Status(ctx, valueOrZero(a.Path)))
		}, Nullable("path", "list all configured servers"), MinLength("path", 1)),
		builtin("lsp_symbols", "Find workspace declarations by name. Returns reusable refs and small source excerpts. Null path searches configured/discovered roots. A path filters where declarations are located, not their callers; use a symbol target for a known call site. Results reflect server build/index scope; use shell search for literal text and unsupported languages. Follow cursor with the same arguments. Search source symbol names, never location handles. A loc_ value is an opaque handle, not a symbol name. Pass an existing handle directly to lsp_inspect, lsp_navigate or lsp_references as ref; do not search for it with this tool.", func(ctx context.Context, _ Call, a lspSymbolArgs) (Result, error) {
			return languageResult(service.Symbols(ctx, lsp.SymbolQuery{Query: a.Query, Path: valueOrZero(a.Path), PageQuery: a.query()}))
		}, rules(pageRules, []Constraint{MinLength("query", 1), Nullable("path", "search configured session roots"), MinLength("path", 1)})...),
		builtin("lsp_outline", "List declarations in one file with reusable refs. Depth defaults to 1 (top-level); maximum 8. Results include identifier ranges and full declaration ranges where the server supplies them.", func(ctx context.Context, _ Call, a lspOutlineArgs) (Result, error) {
			return languageResult(service.Outline(ctx, lsp.OutlineQuery{Path: a.Path, Depth: value(a.Depth, 1), PageQuery: a.query()}))
		}, rules(pageRules, []Constraint{MinLength("path", 1), Nullable("depth", "top-level declarations only"), Minimum("depth", 1), Maximum("depth", 8)})...),
		builtin("lsp_diagnostics", "Read language errors/warnings with explicit freshness and coverage. A nonempty paths list requests checks of those files; null reads cached session publications, never a whole-project clean bill. Unknown/pending or empty cache does not mean no errors. Follow cursor with unchanged arguments.", func(ctx context.Context, _ Call, a lspDiagnosticArgs) (Result, error) {
			return languageResult(service.Diagnostics(ctx, lsp.DiagnosticQuery{Paths: a.Paths, PageQuery: a.query()}))
		}, rules(pageRules, []Constraint{Nullable("paths", "read cached session diagnostics only"), MinItems("paths", 1), MaxItems("paths", 32), MinLength("paths[]", 1)})...),
	}
	inspectRules := []Constraint{Nullable("include_source", "include a bounded source excerpt")}
	inspect, err := ComposeBy("target_kind", provider.ToolDefinition{Name: "lsp_inspect", Description: "Read hover types, documentation and source at the supplied location. To find where something is defined, call lsp_navigate with relation=definition directly; inspection does not follow a call to its definition. References expire when their file changes; rediscover stale references. For a symbol target, supply path, 1-based line, the unqualified symbol and nullable context; Strap calculates the column. Source defaults to included, bounded to 80 lines. An existing loc_ handle needs no discovery step: set input.target_kind to reference and input.ref to the unchanged handle."},
		builtin("inspect_reference", "Use a ref returned by a semantic query.", func(ctx context.Context, _ Call, a lspInspectRef) (Result, error) {
			return languageResult(service.Inspect(ctx, lsp.InspectQuery{Target: a.target(), IncludeSource: value(a.IncludeSource, true)}))
		}, rules(refRules, inspectRules)...),
		builtin("inspect_symbol", "Use an exact identifier on a known source line. Strap calculates its position; context disambiguates repeated identifiers.", func(ctx context.Context, _ Call, a lspInspectSymbol) (Result, error) {
			return languageResult(service.Inspect(ctx, lsp.InspectQuery{Target: a.target(), IncludeSource: value(a.IncludeSource, true)}))
		}, rules(symbolRules, inspectRules)...))
	if err != nil {
		return nil, err
	}
	tools = append(tools, inspect)
	navigateRules := []Constraint{Enum("relation", "definition", "declaration", "type_definition", "implementation")}
	navigate, err := ComposeBy("target_kind", provider.ToolDefinition{Name: "lsp_navigate", Description: "Find where a called or referenced symbol is defined. Call directly with relation=definition; no preceding inspection is needed, including for local or shadowed variables. Also supports declaration, type_definition and implementation relations. Use a returned ref or target_kind=symbol with path, 1-based line, the unqualified identifier and nullable context. Strap calculates the column. Returns locations with source excerpts and new refs. Unsupported capabilities are errors, not empty results. To find a definition from an existing loc_ handle, call this tool directly with input.target_kind=reference, input.ref equal to the unchanged handle, and input.relation=definition. Supply limit and cursor as null for defaults. A handle is not a symbol name; do not search for it with lsp_symbols."},
		builtin("navigate_reference", "Navigate from a returned ref.", func(ctx context.Context, _ Call, a lspNavigateRef) (Result, error) {
			return languageResult(service.Navigate(ctx, lsp.NavigateQuery{Target: a.target(), Relation: a.Relation, PageQuery: a.query()}))
		}, rules(refRules, pageRules, navigateRules)...),
		builtin("navigate_symbol", "Navigate from an exact identifier on a source line; context disambiguates repeated identifiers.", func(ctx context.Context, _ Call, a lspNavigateSymbol) (Result, error) {
			return languageResult(service.Navigate(ctx, lsp.NavigateQuery{Target: a.target(), Relation: a.Relation, PageQuery: a.query()}))
		}, rules(symbolRules, pageRules, navigateRules)...))
	if err != nil {
		return nil, err
	}
	tools = append(tools, navigate)
	referencesRules := []Constraint{Nullable("include_declaration", "exclude the declaration")}
	references, err := ComposeBy("target_kind", provider.ToolDefinition{Name: "lsp_references", Description: "Find semantic usages of a symbol, with source excerpts and reusable location refs. Excludes its declaration by default. Scope depends on the server's workspace/build; strings, reflection and other build targets may require text search. Follow cursor with unchanged arguments. To find usages from an existing loc_ handle, call this tool directly with input.target_kind=reference and input.ref equal to the unchanged handle. Set include_declaration as requested; supply limit and cursor as null for defaults. A handle is not a symbol name; do not search for it with lsp_symbols."},
		builtin("references_reference", "Find usages from a returned ref.", func(ctx context.Context, _ Call, a lspReferencesRef) (Result, error) {
			return languageResult(service.References(ctx, lsp.ReferenceQuery{Target: a.target(), IncludeDeclaration: valueOrZero(a.IncludeDeclaration), PageQuery: a.query()}))
		}, rules(refRules, pageRules, referencesRules)...),
		builtin("references_symbol", "Find usages of an exact identifier on a source line; context disambiguates repeated identifiers.", func(ctx context.Context, _ Call, a lspReferencesSymbol) (Result, error) {
			return languageResult(service.References(ctx, lsp.ReferenceQuery{Target: a.target(), IncludeDeclaration: valueOrZero(a.IncludeDeclaration), PageQuery: a.query()}))
		}, rules(symbolRules, pageRules, referencesRules)...))
	if err != nil {
		return nil, err
	}
	return append(tools, references), nil
}
