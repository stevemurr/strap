package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type errorResponse struct {
	Error Error `json:"error"`
}
type SessionView struct {
	harness.Inspection
	Root identity.ActorID `json:"root"`
}
type CreateRequest struct {
	Config *harness.Config `json:"config,omitempty"`
}
type SendRequest struct {
	To      identity.ActorID `json:"to"`
	Content string           `json:"content"`
}
type AgentRequest struct {
	Parent  identity.ActorID `json:"parent"`
	Profile string           `json:"profile"`
}
type WorkRequest[T any] struct {
	Actor   identity.ActorID `json:"actor"`
	Request T                `json:"request"`
}
type StreamRecord struct {
	Type  string          `json:"type"`
	Event *eventlog.Event `json:"event,omitempty"`
	Error *Error          `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func classify(err error) (int, Error) {
	status, code := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, harness.ErrBusy):
		status, code = 429, "busy"
	case errors.Is(err, ErrUnauthorized), errors.Is(err, work.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, ErrNotFound), errors.Is(err, work.ErrNotFound), errors.Is(err, conversation.ErrAgentNotFound), errors.Is(err, projection.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, harness.ErrClosed), errors.Is(err, conversation.ErrClosed), errors.Is(err, conversation.ErrAgentStopped):
		status, code = 409, "closed"
	case errors.Is(err, work.ErrConflict), errors.Is(err, work.ErrState), errors.Is(err, work.ErrReserved):
		status, code = 409, "conflict"
	case errors.Is(err, eventlog.ErrExpired):
		status, code = 410, "cursor_expired"
	case errors.Is(err, eventlog.ErrDisposed):
		status, code = 410, "disposed"
	case errors.Is(err, eventlog.ErrSession), errors.Is(err, eventlog.ErrPageSize), errors.Is(err, harness.ErrReadQuery), errors.Is(err, eventlog.ErrFuture), errors.Is(err, work.ErrInvalid), errors.Is(err, agent.ErrInvalidQuery):
		status, code = 400, "invalid"
	case errors.Is(err, agent.ErrTokenCountingUnsupported):
		status, code = 501, "unsupported"
	case errors.Is(err, eventlog.ErrCapture):
		status, code = 503, "capture_failed"
	case errors.Is(err, context.DeadlineExceeded):
		status, code = 504, "timeout"
	case errors.Is(err, context.Canceled):
		status, code = 408, "cancelled"
	}
	return status, Error{Code: code, Message: err.Error()}
}
func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		status, e := classify(err)
		writeJSON(w, status, errorResponse{e})
		return
	}
	writeJSON(w, 200, v)
}
func decodeBody[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var v T
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return v, fmt.Errorf("%w: %v", work.ErrInvalid, err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return v, fmt.Errorf("%w: request requires exactly one JSON value", work.ErrInvalid)
	}
	return v, nil
}
func query(r *http.Request) (eventlog.Query, error) {
	q := eventlog.Query{Limit: 100, MaxBytes: 4 << 20}
	var err error
	if value := r.URL.Query().Get("after"); value != "" {
		q.After, err = strconv.ParseUint(value, 10, 64)
		if err != nil {
			return q, fmt.Errorf("%w: after", work.ErrInvalid)
		}
	}
	if value := r.URL.Query().Get("limit"); value != "" {
		q.Limit, err = strconv.Atoi(value)
		if err != nil {
			return q, fmt.Errorf("%w: limit", work.ErrInvalid)
		}
	}
	if value := r.URL.Query().Get("max_bytes"); value != "" {
		q.MaxBytes, err = strconv.Atoi(value)
		if err != nil || q.MaxBytes < 1 || q.MaxBytes > 8<<20 {
			return q, fmt.Errorf("%w: max_bytes", work.ErrInvalid)
		}
	}
	if err = q.Validate(); err != nil {
		return q, fmt.Errorf("%w: %v", work.ErrInvalid, err)
	}
	return q, nil
}
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case s.requests <- struct{}{}:
		defer func() { <-s.requests }()
	default:
		respond(w, nil, harness.ErrBusy)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 1 || parts[0] != "sessions" {
		http.NotFound(w, r)
		return
	}
	id := ""
	if len(parts) > 1 {
		id = parts[1]
	}
	cap := Read
	if r.Method != "GET" {
		cap = Command
	}
	if len(parts) == 1 && r.Method == "POST" {
		cap = Create
	}
	if len(parts) > 2 && parts[2] == "dispose" {
		cap = Dispose
	}
	if len(parts) > 4 && parts[2] == "agents" && parts[4] == "tokens" {
		cap = Measure
	}
	if err := s.options.Authorize(r, cap, id); err != nil {
		respond(w, nil, errors.Join(ErrUnauthorized, err))
		return
	}
	if r.Header.Get("Idempotency-Key") != "" {
		respond(w, nil, fmt.Errorf("%w: idempotency keys are not supported; mutations are never automatically retried", work.ErrInvalid))
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case "GET":
			writeJSON(w, 200, s.ids())
		case "POST":
			req, err := decodeBody[CreateRequest](w, r)
			if err != nil {
				respond(w, nil, err)
				return
			}
			cfg := s.options.DefaultConfig
			if req.Config != nil {
				cfg = *req.Config
			}
			session, err := s.create(r.Context(), cfg)
			if err != nil {
				respond(w, nil, err)
				return
			}
			writeJSON(w, 201, SessionView{session.Inspect(), session.Root()})
		default:
			w.WriteHeader(405)
		}
		return
	}
	session, err := s.lookup(id)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if len(parts) == 2 {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		respond(w, SessionView{session.Inspect(), session.Root()}, nil)
		return
	}
	path := parts[2:]
	if path[0] == "trace" {
		reader, err := session.Trace(r.Context())
		if err != nil {
			respond(w, nil, err)
			return
		}
		defer reader.Close(context.Background())
		http.StripPrefix("/sessions/"+id+"/trace", inspection.Handler(reader)).ServeHTTP(w, r)
		return
	}
	if len(path) == 1 {
		switch path[0] {
		case "agents":
			if r.Method == "GET" {
				respond(w, session.Agents(), nil)
				return
			}
			if r.Method == "POST" {
				req, err := decodeBody[AgentRequest](w, r)
				if err != nil {
					respond(w, nil, err)
					return
				}
				if s.options.AgentProfile == nil {
					respond(w, nil, fmt.Errorf("%w: agent profiles are not configured", work.ErrInvalid))
					return
				}
				spec, err := s.options.AgentProfile(session, req.Profile)
				if err != nil {
					respond(w, nil, err)
					return
				}
				v, err := session.CreateAgent(req.Parent, spec)
				respond(w, v, err)
				return
			}
		case "messages":
			if r.Method == "POST" {
				req, err := decodeBody[SendRequest](w, r)
				if err != nil {
					respond(w, nil, err)
					return
				}
				v, err := session.Send(req.To, req.Content)
				respond(w, v, err)
				return
			}
		case "events":
			if r.Method == "GET" {
				q, err := query(r)
				if err != nil {
					respond(w, nil, err)
					return
				}
				v, err := session.Events(r.Context(), q)
				respond(w, v, err)
				return
			}
		case "close":
			if r.Method == "POST" {
				respond(w, nil, session.Close(r.Context()))
				return
			}
		case "dispose":
			if r.Method == "POST" {
				respond(w, nil, session.Dispose(r.Context()))
				return
			}
		case "flush":
			if r.Method == "POST" {
				respond(w, nil, session.FlushEvents(r.Context()))
				return
			}
		case "logs":
			if r.Method == "POST" {
				entry, err := decodeBody[conversation.DiagnosticEvent](w, r)
				if err == nil {
					err = session.Log(r.Context(), entry)
				}
				respond(w, nil, err)
				return
			}
		}
	}
	if serveRecovery(w, r, session, path) {
		return
	}
	if path[0] == "events" && len(path) == 2 && path[1] == "stream" && r.Method == "GET" {
		s.stream(w, r, session)
		return
	}
	if path[0] == "agents" && len(path) >= 2 {
		serveAgent(w, r, session, path[1:])
		return
	}
	if r.Method == "GET" && len(path) == 2 {
		actor := identity.ActorID(r.URL.Query().Get("actor"))
		var v any
		var err error
		switch path[0] {
		case "receipts":
			receipt, ok := session.Receipt(message.MessageID(path[1]))
			v = receipt
			if !ok {
				err = ErrNotFound
			}
		case "work":
			v, err = session.InspectWork(r.Context(), actor, work.ID(path[1]))
		case "plans":
			v, err = session.GetPlan(r.Context(), actor, work.PlanID(path[1]))
		case "submissions":
			v, err = session.GetSubmission(r.Context(), actor, work.SubmissionID(path[1]))
		case "audits":
			v, err = session.GetAudit(r.Context(), actor, work.AuditID(path[1]))
		default:
			http.NotFound(w, r)
			return
		}
		respond(w, v, err)
		return
	}
	if r.Method == "POST" && len(path) == 2 && path[0] == "work" {
		serveWork(w, r, session, path[1])
		return
	}
	http.NotFound(w, r)
}
func serveAgent(w http.ResponseWriter, r *http.Request, s *harness.Session, path []string) {
	id := identity.ActorID(path[0])
	if len(path) == 1 && r.Method == "GET" {
		opts := conversation.InspectOptions{}
		if r.URL.Query().Get("transcript") == "true" {
			q, err := query(r)
			if err != nil {
				respond(w, nil, err)
				return
			}
			before := uint64(0)
			if x := r.URL.Query().Get("before"); x != "" {
				before, err = strconv.ParseUint(x, 10, 64)
				if err != nil {
					respond(w, nil, fmt.Errorf("%w: before", work.ErrInvalid))
					return
				}
			}
			opts.Transcript = &agent.TranscriptQuery{Before: before, Limit: q.Limit}
		}
		v, err := s.InspectAgent(id, opts)
		respond(w, v, err)
		return
	}
	if len(path) != 2 || r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	var v any
	var err error
	switch path[1] {
	case "pause":
		v, err = s.PauseAgent(id)
	case "resume":
		v, err = s.ResumeAgent(id)
	case "stop":
		v, err = s.StopAgent(id)
	case "tokens":
		req, e := decodeBody[struct {
			Revision uint64 `json:"revision"`
		}](w, r)
		if e != nil {
			respond(w, nil, e)
			return
		}
		n, e := s.CountAgentTokens(r.Context(), id, req.Revision)
		v = struct {
			Count int64 `json:"count"`
		}{n}
		err = e
	default:
		http.NotFound(w, r)
		return
	}
	respond(w, v, err)
}
func workCall[T, R any](w http.ResponseWriter, r *http.Request, fn func(context.Context, identity.ActorID, T) (R, error)) {
	req, err := decodeBody[WorkRequest[T]](w, r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	v, err := fn(r.Context(), req.Actor, req.Request)
	respond(w, v, err)
}
func serveWork(w http.ResponseWriter, r *http.Request, s *harness.Session, action string) {
	switch action {
	case "assign":
		workCall(w, r, s.AssignWork)
	case "reassign":
		workCall(w, r, s.ReassignWork)
	case "cancel":
		workCall(w, r, s.CancelWork)
	case "progress":
		workCall(w, r, s.UpdateProgress)
	case "plan":
		workCall(w, r, s.UpdatePlan)
	case "submit":
		workCall(w, r, s.SubmitWork)
	case "audit":
		workCall(w, r, s.SubmitAudit)
	default:
		http.NotFound(w, r)
	}
}
func (s *Service) stream(w http.ResponseWriter, r *http.Request, session *harness.Session) {
	q, err := query(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if _, err = session.Events(r.Context(), eventlog.Query{After: q.After, Limit: 1}); err != nil {
		respond(w, nil, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		respond(w, nil, errors.New("streaming unsupported"))
		return
	}
	sub, err := session.Subscribe(r.Context(), harness.SubscribeOptions{After: eventlog.Cursor{Session: session.ID(), Sequence: q.After}})
	if err != nil {
		respond(w, nil, err)
		return
	}
	defer sub.Close()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	flusher.Flush()
	enc := json.NewEncoder(w)
	for {
		e, err := sub.Next(r.Context())
		record := StreamRecord{Type: "event", Event: &e}
		if err != nil {
			record = StreamRecord{Type: "end"}
			if !errors.Is(err, io.EOF) {
				_, detail := classify(err)
				record.Type = "error"
				record.Error = &detail
			}
		}
		if enc.Encode(record) != nil {
			return
		}
		flusher.Flush()
		if err != nil {
			return
		}
	}
}
