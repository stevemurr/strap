package inspection

import (
	"fmt"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
	"net/url"
	"strconv"
)

func WorkQuery(q url.Values) (work.ListQuery, error) {
	r := work.ListQuery{Assignee: identity.ActorID(q.Get("assignee")), Kind: work.Kind(q.Get("kind")), State: work.State(q.Get("state")), Cursor: q.Get("cursor")}
	for key, values := range q {
		switch key {
		case "actor", "assignee", "kind", "state", "cursor", "limit":
		default:
			return r, fmt.Errorf("%w: unknown work query field %s", work.ErrInvalid, key)
		}
		if len(values) != 1 {
			return r, fmt.Errorf("%w: duplicate query field", work.ErrInvalid)
		}
	}
	if q.Has("limit") {
		n, e := strconv.Atoi(q.Get("limit"))
		if e != nil || n < 1 || n > 100 {
			return r, fmt.Errorf("%w: limit must be 1–100", work.ErrInvalid)
		}
		r.Limit = n
	}
	if r.Cursor != "" && (q.Has("assignee") || q.Has("kind") || q.Has("state")) {
		return r, fmt.Errorf("%w: cursor cannot be combined with filters", work.ErrInvalid)
	}
	_, err := decodeWorkQuery(r)
	return r, err
}
