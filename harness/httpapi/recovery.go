package httpapi

import (
	"fmt"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
	"net/http"
	"strconv"
)

func readUint(r *http.Request, name string, required bool) (uint64, error) {
	v := r.URL.Query().Get(name)
	if v == "" && !required {
		return 0, nil
	}
	n, e := strconv.ParseUint(v, 10, 64)
	if e != nil {
		return 0, fmt.Errorf("%w: %s", work.ErrInvalid, name)
	}
	return n, nil
}
func readBudget(r *http.Request) (int, error) {
	v := r.URL.Query().Get("max_bytes")
	if v == "" {
		return 64 << 10, nil
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < 1 || n > 1<<20 {
		return 0, fmt.Errorf("%w: max_bytes must be between 1 and 1048576", work.ErrInvalid)
	}
	return n, nil
}
func serveRecovery(w http.ResponseWriter, r *http.Request, s *harness.Session, path []string) bool {
	if r.Method != "GET" {
		return false
	}
	if path[0] == "contents" && len(path) == 2 {
		offset, err := readUint(r, "offset", false)
		if err != nil {
			respond(w, nil, err)
			return true
		}
		budget, err := readBudget(r)
		if err != nil {
			respond(w, nil, err)
			return true
		}
		page, err := s.ReadContent(r.Context(), harness.ContentQuery{ID: identity.ContentID(path[1]), Offset: offset, MaxBytes: budget})
		respond(w, page, err)
		return true
	}
	if path[0] != "outputs" {
		return false
	}
	oq, text, ok, err := inspection.OutputPath(path, r.URL.Query())
	switch {
	case !ok:
		return false
	case err != nil:
		respond(w, nil, err)
	case !text:
		v, err := s.InspectOutput(r.Context(), oq.Output)
		respond(w, v, err)
	default:
		through, err := readUint(r, "through", true)
		if err != nil {
			respond(w, nil, err)
			return true
		}
		v, err := s.ReadOutputText(r.Context(), harness.OutputTextQuery{Channel: oq.Channel, Output: oq.Output, Through: eventlog.Cursor{Session: s.ID(), Sequence: through}, Offset: oq.Offset, MaxBytes: oq.MaxBytes})
		respond(w, v, err)
	}
	return true
}
