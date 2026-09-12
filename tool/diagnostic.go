package tool

import "errors"

// Diagnostic is host-only evidence. It is never appended to model content.
type Diagnostic struct {
	Kind string          `json:"kind"`
	Edit *EditDiagnostic `json:"edit,omitempty"`
}
type EditDiagnostic struct {
	RequestedPath string `json:"requested_path"`
	ResolvedPath  string `json:"resolved_path"`
	Before        string `json:"before"`
	SHA256        string `json:"sha256"`
	Old           string `json:"old"`
	New           string `json:"new"`
}

func (d Diagnostic) Clone() Diagnostic {
	if d.Edit != nil {
		v := *d.Edit
		d.Edit = &v
	}
	return d
}

// DiagnosticError preserves the original error text and errors.Is/As behavior.
type DiagnosticError struct {
	Cause  error
	Detail Diagnostic
}

func (e *DiagnosticError) Error() string { return e.Cause.Error() }
func (e *DiagnosticError) Unwrap() error { return e.Cause }
func DiagnosticFrom(err error) *Diagnostic {
	var e *DiagnosticError
	if !errors.As(err, &e) {
		return nil
	}
	d := e.Detail.Clone()
	return &d
}
