package eventlog

import "github.com/stevemurr/strap/identity"

type ContentRef struct {
	ID     identity.ContentID `json:"id"`
	First  Cursor             `json:"first"`
	Last   Cursor             `json:"last"`
	Bytes  uint64             `json:"bytes"`
	SHA256 string             `json:"sha256"`
}
