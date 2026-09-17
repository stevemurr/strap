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
| `GET /agents` | Agent state, lifecycle revision, role, registration, eligible work kinds, and active work IDs |
| `POST /agents` | `{ "actor": "agent-1", "request": {"role":"implementor"} }`; root only; `implementor` or `auditor`, returns idle registration |
| `GET /agents/{agent}` | Inspect; `?transcript=true&before=N&limit=N` requests history |
| `POST /agents/{agent}/pause`, `/resume`, `/stop` | Agent lifecycle controls; `/stop` permanently terminates the agent |
| `POST /agents/{agent}/tokens` | `{ "revision": N }`; explicit provider I/O (`measure` capability) |
| `POST /messages` | `{ "to": "agent-1", "content": "..." }` |
| `GET /receipts/{message}` | Delivery receipt |
| `POST /work/assign`, `/reassign`, `/cancel`, `/progress`, `/plan`, `/submit`, `/audit` | `{ "actor": "agent-1", "request": ... }`; request is the corresponding public `work` type |
| `GET /work?actor=...` | Root-only work discovery across all states; optional assignee/kind/state, limit 1–100 (default 20), or cursor plus optional limit |
| `GET /work/{work}?actor=...` | Work inspection, including related submission/audit evidence |
| `GET /plans/{plan}?actor=...` | Plan snapshot |
| `GET /submissions/{submission}?actor=...` | Submission snapshot |
| `GET /audits/{audit}?actor=...` | Audit snapshot |
| `GET /events?after=N&limit=N&max_bytes=N` | Finite retained page, accepted head, exclusive cursor and seal outcome |
| `GET /outputs/{agent}/{call}` | Output metadata, applied cursor and source health |
| `GET /outputs/{agent}/{call}/text?channel=content|reasoning&through=N&offset=N&max_bytes=N` | UTF-8 text page at a fixed session prefix; `through` is required |
| `GET /contents/{id}?offset=N&max_bytes=N` | Immutable content byte page; JSON data uses base64 |
| `GET /events/stream?after=N` | Independent NDJSON subscription |
| `POST /logs` | `{ "level": "info", "message": "...", "fields": {"key":"value"} }` |
| `POST /flush` | Ordered publication barrier; does not sync disk |
| `POST /interrupt` | Stop current work across all agents; retain the conversation for new input |
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

`POST /interrupt` takes no body and returns `null` after agents, tool results,
queued deliveries, and outstanding delegated work settle. The session stays open.
The next accepted `/messages` request starts a fresh exchange with retained
history. Pending mutations return 409 `interrupted`; after settlement, work
mutations and `/resume` remain unavailable until new input. A timeout or client
disconnect stops only the wait; another `/interrupt` joins the same attempt.
It never rolls back edits or automatically retries tools. Use `/close` to end the
session, or the per-agent `/stop` route for permanent termination.

Event cursors are exclusive and start at zero. Pages default to 100 entries, with
a maximum of 1,000 and a default 4 MiB byte budget; transcript pages have a maximum
of 100. History does not expire while the session is retained. Disposed storage
returns 410, future/invalid cursors return 400, work revision conflicts return 409, and capture
failure returns 503. Errors have `{"error":{"code":"...","message":"..."}}`.
HTTP admission defaults to 256 simultaneous requests (including streams), configurable
through `Options.MaxRequests`; excess admission returns 429 `busy`.
Request JSON is limited to 1 MiB, rejects unknown fields, and contains one value.

A stream emits `{"type":"event","event":...}` records, then `{"type":"end"}`
after a clean seal. A failure after headers emits `{"type":"error","error":...}`.
Reconnect after the last event successfully applied by the view. The session ID
in the route scopes the cursor. Closing the connection only detaches that reader.
Capture failure drains the readable prefix, reports an error, and cancels execution.
Output/content reads require the same named-session read authorization as events.
They default to 64 KiB pages and allow at most 1 MiB.

Schema 3 retains `output_started`, `output_delta`, `output_finished`,
`history_appended`, and `content_chunk`. Each output delta explicitly identifies
`channel: "content"` or `channel: "reasoning"`, with independent UTF-8 byte offsets.
Output inspection and finish records include `reasoning_bytes`; existing
`text_bytes` / `bytes` fields still count answer content. Text reads default to
`content` and accept `channel=reasoning`; an unknown channel returns 400.
Reasoning is retained for inspection and never added to model conversation history. Full payload framing, correlation, cancellation, archive compatibility,
and Go subscription examples are specified in [the recovery contract](../RECOVERY.md).

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

Agent creation and assignment are separate operations. Every assignment/reassignment
requires an existing `assignee`. Assignment is a strict union: implementation accepts
task/context/expected_output/scope; audit requires original work_id/expected_revision/
submission_id; repair requires original work_id/expected_revision/audit_id. Fields
from another branch are rejected even when empty or null. Creation, assignment,
and reassignment retain raw request JSON for the same pure decoder used by tools.
The old parent/profile creation envelope and AgentProfile callback are removed.

`submit_audit` records a verdict without creating repairs. On failure the root
explicitly assigns repair work. `GET /trace/work?actor=...` and the standalone
inspection handler offer the same fixed-prefix listing. Continuations must use
only cursor and optional limit (plus actor); filters are preserved by the cursor.
Unavailable prefixes fail explicitly. Listing is discovery, not deduplication.
