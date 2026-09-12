# Harness HTTP API

`httpapi.Service` is a `net/http.Handler` and owns its sessions. It calls the same
Go methods used by direct hosts and model-tool adapters. The host supplies an
`Authorize(request, capability, sessionID)` callback and may supply a session
factory with injected providers/tools. No HTTP handler selects work policy.

For local use:

```sh
export STRAP_API_TOKEN='choose-a-local-token'
go run ./cmd/strap -listen 127.0.0.1:8080 -web=false
curl -H "Authorization: Bearer $STRAP_API_TOKEN" \
  -H 'Content-Type: application/json' -d '{}' \
  http://127.0.0.1:8080/sessions
```

The response contains `id` and `root`. Use them for subsequent calls:

```sh
curl -H "Authorization: Bearer $STRAP_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"to":"agent-1","content":"Inspect the workspace"}' \
  http://127.0.0.1:8080/sessions/SESSION_ID/messages
curl -N -H "Authorization: Bearer $STRAP_API_TOKEN" \
  'http://127.0.0.1:8080/sessions/SESSION_ID/events/stream?after=0'
```

CLI HTTP mode requires a literal loopback IP and a nonempty token. Embed the
handler behind your own TLS listener and authorization policy for remote use.
The bearer helper grants trusted host capabilities. It is not a per-agent
permission system; authorized command callers may select existing work actors.
Authorize by capability and session ID if different callers have different access.

All paths below are relative to `/sessions/{id}` unless shown in full.

| Method and path | Operation / JSON body |
|---|---|
| `GET /sessions` | List registered session IDs |
| `POST /sessions` | Create; `{}` uses host defaults, `{"config": ...}` supplies a complete `harness.Config` |
| `GET /sessions/{id}` | State, root ID, capture health/coverage and effective configuration |
| `GET /agents` | Agent state snapshots and lifecycle revisions |
| `POST /agents` | `{ "parent": "agent-1", "profile": "name" }`; requires a host `AgentProfile` resolver |
| `GET /agents/{agent}` | Inspect; `?transcript=true&before=N&limit=N` requests history |
| `POST /agents/{agent}/pause`, `/resume`, `/stop` | Lifecycle controls |
| `POST /agents/{agent}/tokens` | `{ "revision": N }`; explicit provider I/O (`measure` capability) |
| `POST /messages` | `{ "to": "agent-1", "content": "..." }` |
| `GET /receipts/{message}` | Delivery receipt |
| `POST /work/assign`, `/reassign`, `/cancel`, `/progress`, `/plan`, `/submit`, `/audit` | `{ "actor": "agent-1", "request": ... }`; request is the corresponding public `work` type |
| `GET /work/{work}?actor=...` | Work inspection, including related submission/audit evidence |
| `GET /plans/{plan}?actor=...` | Plan snapshot |
| `GET /submissions/{submission}?actor=...` | Submission snapshot |
| `GET /audits/{audit}?actor=...` | Audit snapshot |
| `GET /events?after=N&limit=N` | Finite retained page, bounds, cursor and seal outcome |
| `GET /events/stream?after=N` | Independent NDJSON subscription |
| `POST /logs` | `{ "level": "info", "message": "...", "fields": {"key":"value"} }` |
| `POST /flush` | Ordered publication barrier; does not sync disk |
| `POST /close` | Finalize execution and capture; retain inspection/history |
| `POST /dispose` | Finalize and release event storage (`dispose` capability) |

Configuration uses snake_case JSON fields. Durations have `_ns` suffixes and are
integer nanoseconds. Config replacement is not a partial merge. Executable
providers, tools, resources, and authorization callbacks are host dependencies,
not wire values. The factory receives service lifetime, so a disconnected create
request cannot silently kill the session it just created. Use the session list to
reconcile an uncertain create response. `Service.Close` disposes registered
sessions and retains retryable failed-startup cleanup handles.

Mutation requests are executed once per received request. The adapter does not
retry them and does not implement idempotency-key deduplication; it rejects
`Idempotency-Key` instead of implying protection it cannot provide. Work revision
checks reject stale transitions. A timeout/disconnect may occur after mutation:
inspect state or receipts before deciding whether to send another request.

Event cursors are exclusive and start at zero. Pages default to 100 entries, with
a maximum of 1,000; transcript pages have a maximum of 100. Expired cursors return
410, future cursors return 400, work revision conflicts return 409, and capture
failure returns 503. Errors have `{"error":{"code":"...","message":"..."}}`.
Request JSON is limited to 1 MiB, rejects unknown fields, and contains one value.

A stream emits `{"type":"event","event":...}` records, then `{"type":"end"}`
after a clean seal. A failure after headers emits `{"type":"error","error":...}`.
Reconnect using the last event sequence received. Closing the connection only
detaches that reader. Capture failure is not a clean end and does not disable
session commands. Retention gaps remain explicit even after session closure.

Run `go test -race ./harness/httpapi` for the direct/HTTP audit-repair parity,
authorization, revision, paging and real connection disconnect/reconnect tests.

Run the opt-in smoke test against a live vLLM model with:

```sh
STRAP_LIVE_BASE_URL=http://192.168.1.237:8355 \
  go test -race ./harness/httpapi -run '^TestLiveModelHTTP$' -count=1 -timeout 3m -v
```

`STRAP_LIVE_MODEL` optionally overrides `qwen3.6`. This starts a real local HTTP
server using the real provider factory and a temporary workspace. It checks a
plain reply, observer disconnect/reconnect, a file-read tool round trip, usage and
automatic context counting, contiguous streamed events, finite-page agreement,
clean JSONL sealing, and disposal. It uses a short test prompt, disables thinking
and web tools, and limits each completion to 512 tokens. Ordinary tests skip it
when `STRAP_LIVE_BASE_URL` is unset.
