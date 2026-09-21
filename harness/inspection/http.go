package inspection

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

// Handler serves read-only inspection of a borrowed reader. The embedding host
// must authorize access before calling it. It never opens caller-supplied paths.
func Handler(reader *Reader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(value any, err error) {
			if err != nil {
				status, code := 500, "internal"
				switch {
				case errors.Is(err, work.ErrForbidden):
					status, code = 403, "forbidden"
				case errors.Is(err, work.ErrInvalid):
					status, code = 400, "invalid"
				case errors.Is(err, projection.ErrNotFound):
					status, code = 404, "not_found"
				case errors.Is(err, ErrClosed), errors.Is(err, eventlog.ErrDisposed):
					status, code = 410, "closed"
				case errors.Is(err, ErrReadQuery), errors.Is(err, eventlog.ErrSession), errors.Is(err, eventlog.ErrFuture), errors.Is(err, eventlog.ErrPageSize):
					status, code = 400, "invalid"
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": eventlog.Problem{Code: code, Message: err.Error()}})
				return
			}
			_ = json.NewEncoder(w).Encode(value)
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		head, err := reader.Head(r.Context())
		if err != nil {
			write(nil, err)
			return
		}
		q := r.URL.Query()
		if strings.Trim(r.URL.Path, "/") == "work" {
			query, e := WorkQuery(q)
			if e != nil {
				write(nil, e)
				return
			}
			v, e := reader.ListWork(r.Context(), identity.ActorID(q.Get("actor")), query)
			write(v, e)
			return
		}
		number := func(key string, def uint64) (uint64, error) {
			if !q.Has(key) {
				return def, nil
			}
			n, err := strconv.ParseUint(q.Get(key), 10, 64)
			if err != nil {
				return 0, ErrReadQuery
			}
			return n, nil
		}
		through, err := number("through", head.Cursor.Sequence)
		if err != nil {
			write(nil, err)
			return
		}
		after, err := number("after", 0)
		if err != nil {
			write(nil, err)
			return
		}
		limit, err := number("limit", 100)
		if err != nil || limit < 1 || limit > 1000 {
			write(nil, ErrReadQuery)
			return
		}
		cursor := eventlog.Cursor{Session: head.Cursor.Session, Sequence: through}
		if q.Has("session") && q.Get("session") != cursor.Session {
			write(nil, eventlog.ErrSession)
			return
		}
		view, err := reader.At(r.Context(), cursor)
		if err != nil {
			write(nil, err)
			return
		}
		page := PageQuery{After: after, Limit: int(limit)}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch parts[0] {
		case "", "session":
			if len(parts) == 1 {
				v, e := view.Session(r.Context())
				write(v, e)
				return
			}
		case "tools":
			if len(parts) == 1 {
				v, e := view.ListTools(r.Context(), ToolQuery{PageQuery: page, Agent: identity.ActorID(q.Get("agent")), Name: q.Get("name")})
				write(v, e)
				return
			}
			v, e := view.InspectTool(r.Context(), identity.ToolInvocationID(strings.Join(parts[1:], "/")))
			write(v, e)
			return
		case "agents":
			if len(parts) == 1 {
				v, e := view.ListAgents(r.Context(), page)
				write(v, e)
				return
			}
			if len(parts) == 2 {
				v, e := view.InspectAgent(r.Context(), identity.ActorID(parts[1]))
				write(v, e)
				return
			}
		case "outputs":
			if len(parts) == 1 {
				v, e := view.ListOutputs(r.Context(), OutputQuery{PageQuery: page, Agent: identity.ActorID(q.Get("agent"))})
				write(v, e)
				return
			}
			if oq, text, ok, e := OutputPath(parts, q); ok {
				switch {
				case e != nil:
					write(nil, e)
				case text:
					v, e := view.ReadOutputText(r.Context(), oq)
					write(v, e)
				default:
					v, e := view.InspectOutput(r.Context(), oq.Output)
					write(v, e)
				}
				return
			}
		case "records":
			if len(parts) == 2 {
				seq, e := strconv.ParseUint(parts[1], 10, 64)
				if e != nil {
					write(nil, ErrReadQuery)
					return
				}
				v, e := view.ReadRecord(r.Context(), eventlog.Cursor{Session: cursor.Session, Sequence: seq})
				write(v, e)
				return
			}
		}
		write(nil, projection.ErrNotFound)
	})
}

// OutputPath parses /outputs/{agent}/{call}[/text] with its channel, offset and
// max_bytes query. ok is false when parts has another shape.
func OutputPath(parts []string, q url.Values) (query OutputTextQuery, text, ok bool, err error) {
	if !(len(parts) == 3 || len(parts) == 4 && parts[3] == "text") {
		return
	}
	ok, text = true, len(parts) == 4
	call, e := strconv.ParseUint(parts[2], 10, 64)
	if e != nil || call == 0 {
		return query, text, ok, fmt.Errorf("%w: output call", ErrReadQuery)
	}
	query = OutputTextQuery{Output: identity.OutputID{Agent: identity.ActorID(parts[1]), Call: call}, Channel: provider.OutputChannel(q.Get("channel")), MaxBytes: 64 << 10}
	if !text {
		return
	}
	if q.Has("offset") {
		if query.Offset, e = strconv.ParseUint(q.Get("offset"), 10, 64); e != nil {
			return query, text, ok, fmt.Errorf("%w: offset", ErrReadQuery)
		}
	}
	if q.Has("max_bytes") {
		n, e := strconv.Atoi(q.Get("max_bytes"))
		if e != nil || n < 1 || n > 1<<20 {
			return query, text, ok, fmt.Errorf("%w: max_bytes must be between 1 and 1048576", ErrReadQuery)
		}
		query.MaxBytes = n
	}
	return
}
