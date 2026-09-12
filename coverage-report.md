# Web tool coverage

Measured on 2026-09-11 with Go 1.27.0 on macOS/arm64. The complete compiled Go
path for `web_search` and `open_url` has **100% statement coverage: 680 / 680**.

| Source | Covered / total statements |
| --- | ---: |
| `tool/web_search.go` | 90 / 90 |
| `tool/open_url.go` | 109 / 109 |
| `tool/web.go` | 72 / 72 |
| `internal/agentbrowser/client.go` | 103 / 103 |
| `internal/agentbrowser/cleanup_unix.go` | 63 / 63 |
| `internal/webkit/worker.go` | 197 / 197 |
| `internal/webprocess/process.go` | 37 / 37 |
| `internal/webprocess/process_unix.go` | 9 / 9 |

Run `sh scripts/check-web-coverage.sh` to repeat the offline tests with the race
detector and fail on any uncovered statement in these tool files or backend
packages. The check includes every source file compiled for the host platform
in those backend packages; it does not round percentages to decide whether to
pass. The complete repository also passed `go test -race ./...`.

Tests exercise argument and response limits, rendered-page validation, snapshot
continuation and eviction, worker protocol failures, startup and queue
cancellation, and cleanup failures. OS operations can be replaced per client or
cleanup call so error tests never signal a real browser. These Go coverage
numbers do not measure the embedded DOM JavaScript, external browser binaries,
or other platforms. Live browser and model checks remain opt-in; see the web
tools section of [README.md](README.md).

# Historical package coverage

The following snapshot predates the web tool coverage work and is retained for
comparison. Its package percentages are not the current repository totals.

Measured on 2026-09-11 (macOS/arm64), using Go statement coverage.

Validation: `go test -race ./... -coverprofile=/tmp/strap-coverage-final.out -timeout=90s`.

Every package with executable statements exceeds 95%, using the exact statement counts rather than rounded percentages. No packages or source files were excluded.

| Package | Before | After | Covered / total statements |
| --- | ---: | ---: | ---: |
| `agent` | 50.4% | 96.99% | 258 / 266 |
| `cmd/strap` | 78.4% | 95.81% | 160 / 167 |
| `content` | 25.9% | 100.00% | 27 / 27 |
| `conversation` | 95.3% | 95.34% | 184 / 193 |
| `examples/delegation` | 0.0% | 97.00% | 97 / 100 |
| `examples/files` | 0.0% | 97.62% | 41 / 42 |
| `examples/local` | 0.0% | 97.56% | 40 / 41 |
| `examples/pdf` | 0.0% | 96.08% | 49 / 51 |
| `examples/shell` | 0.0% | 97.30% | 36 / 37 |
| `inbox` | 61.2% | 100.00% | 49 / 49 |
| `internal/tui` | 90.5% | 97.99% | 925 / 944 |
| `internal/workflow` | 76.2% | 95.40% | 228 / 239 |
| `message` | 54.5% | 100.00% | 22 / 22 |
| `prompt` | 100.0% | 100.00% | 4 / 4 |
| `provider` | 0.0% | 100.00% | 22 / 22 |
| `provider/chatcompletions` | 82.4% | 100.00% | 17 / 17 |
| `provider/internal/chatwire` | 0.0% | 100.00% | 111 / 111 |
| `provider/vllm` | 95.4% | 95.38% | 62 / 65 |
| `tool` | 86.6% | 95.20% | 972 / 1021 |
| `work` | 78.3% | 98.49% | 458 / 465 |
| `identity` | N/A | N/A | No executable statements |

The full suite passed with the race detector. HTTP fixtures use local test servers; no live model server is required. PDF rendering tests use Poppler (`pdfinfo` and `pdftoppm`).

Tests also exposed and fixed a PDF output-limit bypass: the embedded `bytes.Buffer.ReadFrom` fast path skipped the bounded `Write` method. The buffer now routes that fast path through its bounded writer.
