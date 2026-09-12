package record

import (
	"encoding/json"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/identity"
)

type ContentChunk struct {
	ID     identity.ContentID `json:"id"`
	Offset uint64             `json:"offset"`
	Data   []byte             `json:"data"`
}

// Framed preserves the complete payload in the same log. Control carries only
// bounded transition metadata, so Apply never has to download the content body.
type Framed struct {
	Content *eventlog.ContentRef `json:"content_ref,omitempty"`
	Control json.RawMessage      `json:"control,omitempty"`
}

func Frame(raw json.RawMessage) (Framed, bool) {
	var f Framed
	if json.Unmarshal(raw, &f) != nil || f.Content == nil {
		return Framed{}, false
	}
	return f, true
}
