package conversation

import "github.com/stevemurr/strap/research"

// ResearchEvent is retained host observation; it never enters an agent inbox.
type ResearchEvent struct {
	Event research.Event `json:"event"`
}

func (ResearchEvent) isEvent() {}
