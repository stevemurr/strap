# Tool path grounding comparison, September 21, 2026

This experiment compares two changes motivated by the existing path failures:
clearer local-tool descriptions and shared workspace grounding. It is a focused
selection/recovery probe against synthetic files, not a full agent-task benchmark.
The UTC date is September 21; the local date at the start was September 20.

The evaluated candidate adds workspace guidance to root, implementor, auditor and
researcher prompts independently of whether LSP is enabled. It favors relative
paths, preserves filenames in assignments, separates package names from
filesystem layout, directs directory discovery to shell, and permits intentional
new files after checking their parent. File/PDF/shell descriptions give the same
operation-specific guidance. The LSP source-path description favors observed
session-relative paths. No fuzzy path repair or filesystem resolution changes
are introduced.

## Experiment

The four variants are baseline, descriptions only, grounding only, and both.
The baseline tool definitions are frozen in
`harness/testdata/path-tools-baseline.json`. An offline test checks that candidate
and baseline schemas differ only in descriptions. All variants use the same
fixture directory, filenames, user prompts, tool ordering, and generation settings.
Variant order rotates by case and repetition; requests are sequential.

There are eight cases, each repeated three times per variant (96 trials total):

- Read README.md using a long, supplied workspace root.
- List the workspace directory with the appropriate tool.
- Inspect workspace-root prefix.go in package config, with config/prefix.go as a decoy.
- Read the unquoted path preview/emoji file.go, with preview/emoji/file.go as a decoy.
- Navigate from that spaced filename using real gopls.
- Resolve a conflicting worker assignment against a preceding real source receipt.
- Create a new workspace-root file, without overrestricting new paths.
- Prepare the missing parent directory of a requested new file.

The handoff case tests a receiving agent's selection from conflicting instructions;
it does not test an actual delegating agent's generation of an assignment.
The fixture contains wrong-but-existing files so successful execution alone
cannot establish correct targeting. Source fixtures must remain unchanged.
New-file outputs are removed between trials. The shell's HOME points at the
synthetic fixture to keep accidental home-directory discovery within that fixture.

The server is the existing user-supplied vLLM deployment, advertised model
`qwen3.6` (`unsloth/Qwen3.6-35B-A3B-NVFP4-Fast`). Sampling is temperature 1,
top-p 0.95, top-k 20, repetition penalty 1.1, thinking enabled, with 4,096 maximum
output tokens per request. Each trial allows at most three model calls and each
call has a 90-second deadline. No server settings were changed.

Raw artifacts retain the baseline/candidate schemas, system instructions, fixture
contents, prompts, responses, arguments, actual tool results, errors and usage.
Recovery receives the real tool result/error, without an oracle-provided correct
path. Trials stop at a recognized correct operation, successful wrong-target
operation, response without exactly one tool call, or the call limit.

## Evaluator correction

The initial batch had an invalid frozen baseline root: replacing the temporary
path before canonicalizing macOS's /private prefix left an extra /private prefix.
The batch was interrupted, preserved, and excluded from the comparison. A
regression check rejects that malformed placeholder. The corrected batch is
retained separately; model failures are not discarded or rerun to replace scores.

## Reproduction

```sh
STRAP_LIVE_PATHS=1 \
STRAP_LIVE_BASE_URL=https://spark-0368.tail11899.ts.net:8443 \
STRAP_LIVE_MODEL=qwen3.6 \
STRAP_LIVE_PATH_OUTPUT="$PWD/eval/results/path-configurations-rerun" \
go test ./harness -run '^TestLivePathConfigurations$' -count=1 -timeout 30m -v
```

Install gopls before running. Ordinary tests skip live model requests. Local
artifacts are under ignored `eval/results/2026-09-21-path-configurations/`, with
invalid initial artifacts at the top level and the valid batch in `corrected/`.

## Results and decision

**Do not promote either candidate as a demonstrated path-reliability fix.**
Descriptions alone gained one correct first call but lost one eventual correct
target and added one wrong-target success. Shared grounding and the combined
variant performed worse in this small sample. Production wording was restored;
the two frozen tool-definition fixtures and the candidate grounding constant
remain in the opt-in probe. The temporary production patch is preserved with the
artifacts as `candidate-production.patch`.

| Configuration | Correct first call | Correct before wrong target, within 3 calls | Wrong-target success before correct target | Unresolved | Actual model calls |
| --- | ---: | ---: | ---: | ---: | ---: |
| Baseline | 15/24 | 19/24 | 5/24 | 0/24 | 36 |
| Descriptions only | 16/24 | 18/24 | 6/24 | 0/24 | 35 |
| Grounding only | 13/24 | 15/24 | 9/24 | 0/24 | 34 |
| Both | 10/24 | 15/24 | 8/24 | 1/24 | 39 |

There were 144 model calls, no provider errors, and 14 rejected tool executions
(3/4/4/3 by variant). The corrected batch took 363 seconds. Fewer calls can reflect
an early wrong-file success, so call count alone is not a quality measure.

Correct first calls by case:

| Case | Baseline | Descriptions | Grounding | Both |
| --- | ---: | ---: | ---: | ---: |
| Workspace-root read | 3/3 | 3/3 | 3/3 | 3/3 |
| Directory discovery | 3/3 | 3/3 | 3/3 | 2/3 |
| Package versus directory | 1/3 | 1/3 | 0/3 | 0/3 |
| Spaced filename read | 0/3 | 1/3 | 0/3 | 1/3 |
| Spaced filename navigation | 2/3 | 0/3 | 1/3 | 1/3 |
| Conflicting handoff | 1/3 | 3/3 | 0/3 | 1/3 |
| New file | 3/3 | 2/3 | 3/3 | 0/3 |
| Missing parent | 2/3 | 3/3 | 3/3 | 2/3 |

The combined variant often added discovery before an otherwise direct operation;
its three new-file cases all eventually succeeded, but none on the first call.
Discovery did not reliably resolve ambiguity: after finding both prefix.go and
config/prefix.go, the model sometimes chose the package-shaped decoy. With a
wrong spaced path, source-context errors sometimes caused the model to alter the
context string instead of rechecking the path, then read the decoy successfully.

### Scoring correction without resampling

The original scorer was too strict about tool choice: it required read_file even
when lsp_inspect, an outline containing the actual function body, or shell output
provided the requested source. It also failed to count wrong-target inspection.
The original direct-call counts were 14/13/13/9; the corresponding corrected
counts are 15/16/13/10. Original responses and scores remain untouched.

The table above applies the corrected semantic-evidence rubric uniformly to all
96 retained trials. Equivalent source reads count; discovery alone does not.
The first decisive correct-source or decoy-source result determines the outcome.
Any calls recorded after that point still count in actual model-call totals.
One combined-variant trial emitted two calls when exactly one was requested;
the probe did not execute that batch and counts it as unresolved.

The current probe fixes the scorer, with offline tests for equivalent inspection,
wrong-file inspection, preparatory reads, and unchanged argument schemas.
`summary.json`, `rescore.py`, and `probe-source-original.go` beside the raw traces
retain the correction and the original evaluator. Future runs stop at the first
correct or wrong source result under the corrected rubric, so their recovery
call counts are not an exact replay of the original scorer's stopping behavior.

### Limits and next step

This is three repetitions per case on one model, with a deliberately challenging
fixture and unquoted spaced paths. Variant order rotates, but sampling seeds are
not paired and the shared gopls instance warms over the run. The baseline system
prompt is a focused probe instruction, not the complete production role prompt.
These counts neither prove general regression nor establish a statistically
reliable winning configuration. Directory and root-path cases already passed
almost universally, leaving little room to measure gains there. The source-read
cases measure evidence acquisition, not correctness of a final explanation.

The next experiment should test literal path presentation and structured observed
file selections, especially with wrong-but-existing decoys and conflicting
handoffs. The results do not support adding more prose as the sole remedy.

## Validation

Affected package tests passed for tool, harness, internal/tui and
internal/lspconfig; their localhost HTTP fixtures required running outside the
network sandbox. Focused oracle/schema tests and go vet for those packages also
passed. No live provider settings changed, no production path-resolution behavior
changed, and unrelated workspace changes were preserved.
