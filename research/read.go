package research

// ReadQuery selects retained research. Live readers check current work visibility
// on every page; cursors pin an immutable accepted prefix.
type ReadQuery struct {
	Mode     string `json:"mode"`
	WorkID   string `json:"work_id,omitempty"`
	ReportID string `json:"report_id,omitempty"`
	SourceID string `json:"source_id,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}
