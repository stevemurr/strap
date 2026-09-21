#!/usr/bin/env bash
# Host orchestration only: each problem still runs in its own fixed-path container.
set -eo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
usage() {
  cat <<'USAGE'
Usage: scripts/eval.sh [options] PROBLEM... [-- model flags]
       scripts/eval.sh [options] --tier easy,medium,hard [-- model flags]
       scripts/eval.sh [options] --all [-- model flags]
       scripts/eval.sh [options] --list

Options:
  --no-build       Use the existing strap-eval image (default: cached build)
  --config FILE    Host model catalog (default: host Strap catalog, if present)
  --out DIR        New host directory for this batch (default: eval/results/container-TIMESTAMP-XXXXXX)
  --tier TIERS     Run all problems in these comma-separated tiers
  --all            Run the whole ladder
  --list           List problems without contacting a model
  -h, --help       Show this help

Examples:
  scripts/eval.sh easy-01-budget-pair
  scripts/eval.sh --no-build --tier easy -- -profile qwen3.6
  scripts/eval.sh easy-01-budget-pair easy-18-shared-key-prefix -- -base-url https://MODEL_HOST:8443

Runs sequentially. Each problem gets its own workspace, outbox and results.
Agent output is graded in a fresh container with networking disabled.
A failed solution is a completed grade; infrastructure failures exit nonzero.
USAGE
}
fail() { printf 'eval.sh: %s\n' "$*" >&2; exit 1; }
need_value() { [[ $# -ge 2 && -n "$2" ]] || fail "$1 needs a value"; }

build=true
config=""
out=""
tiers=""
all=false
list=false
problems=()
model_args=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --no-build) build=false; shift ;;
    --config) need_value "$@"; config=$2; shift 2 ;;
    --out) need_value "$@"; out=$2; shift 2 ;;
    --tier) need_value "$@"; tiers=$2; shift 2 ;;
    --all) all=true; shift ;;
    --list) list=true; shift ;;
    --) shift; model_args=("$@"); break ;;
    -*) fail "unknown option $1; put model flags after --" ;;
    *) problems+=("$1"); shift ;;
  esac
done
if [[ -n "$tiers" ]] && $all; then fail 'choose --tier or --all'; fi
if [[ ${#problems[@]} -gt 0 ]] && { [[ -n "$tiers" ]] || $all; }; then
  fail 'choose problem IDs, --tier, or --all'
fi
if ! $list && ! $all && [[ -z "$tiers" && ${#problems[@]} -eq 0 ]]; then usage >&2; exit 1; fi
for arg in "${model_args[@]}"; do
  case "$arg" in
    -problem|--problem|-problem=*|--problem=*|-config|--config|-config=*|--config=*)
      fail 'select problems and --config before --' ;;
  esac
done
command -v container >/dev/null 2>&1 || fail "Apple's container CLI is required"
if [[ -n "$config" ]]; then
  [[ -f "$config" ]] || fail "model catalog not found: $config"
else
  candidate=${XDG_CONFIG_HOME:-$HOME/.config}/strap/models.json
  if [[ -f "$candidate" ]]; then config=$candidate; fi
fi
if $build; then
  container build -f "$repo/eval/Dockerfile" -t strap-eval "$repo"
fi
selection=()
if [[ -n "$tiers" ]]; then selection=(-tier "$tiers"); fi
catalog=$(container run --rm --progress none strap-eval list "${selection[@]}")
if $list; then printf '%s\n' "$catalog"; exit 0; fi
available=()
while read -r _tier id _rest; do
  [[ -z "$id" ]] || available+=("$id")
done <<< "$catalog"
if $all || [[ -n "$tiers" ]]; then problems=("${available[@]}"); fi
[[ ${#problems[@]} -gt 0 ]] || fail 'no problems selected'
seen=' '
for problem in "${problems[@]}"; do
  found=false
  for id in "${available[@]}"; do [[ "$problem" != "$id" ]] || found=true; done
  $found || fail "unknown problem: $problem"
  [[ "$seen" != *" $problem "* ]] || fail "duplicate problem: $problem"
  seen+="$problem "
done

if [[ -n "$out" ]]; then
  [[ ! -e "$out" ]] || fail "output already exists: $out (use a new directory)"
  mkdir -p -- "$(dirname -- "$out")"
  mkdir -- "$out"
else
  mkdir -p "$repo/eval/results"
  out=$(mktemp -d "$repo/eval/results/container-$(date +%Y%m%d-%H%M%S)-XXXXXX")
fi
out=$(cd -- "$out" && pwd -P)
# Snapshot private fixtures for repeatable grading; this directory is never mounted in the agent.
cp -R "$repo/eval/ladder" "$out/grading"
config_mount=()
config_args=()
if [[ -n "$config" ]]; then
  mkdir "$out/config"
  cp -- "$config" "$out/config/models.json"
  config_mount=(--mount "type=bind,source=$out/config,target=/config,readonly")
  config_args=(-config /config/models.json)
fi
printf '%s\n' "Results: $out"
printf '%s\n' "${problems[@]}" > "$out/problems.txt"
# Unique names let interruption clean up only containers started by this invocation.
active=''
cleanup() {
  if [[ -n "$active" ]]; then
    container stop "$active" >/dev/null 2>&1 || true
    container delete "$active" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
failed=0
index=0
for problem in "${problems[@]}"; do
  index=$((index + 1))
  attempt=$out/$problem
  mkdir -p "$attempt/workspace" "$attempt/results" "$attempt/outbox"
  printf '\n[%d/%d] %s\n' "$index" "${#problems[@]}" "$problem"
  active="strap-eval-$$-$index-agent"
  if ! container run --rm --progress none --name "$active" --cpus 2 --memory 2g \
      --mount "type=bind,source=$attempt/workspace,target=/workspace" \
      --mount "type=bind,source=$attempt/results,target=/results" \
      --mount "type=bind,source=$attempt/outbox,target=/outbox" \
      "${config_mount[@]}" strap-eval -problem "$problem" -q "${config_args[@]}" "${model_args[@]}" \
      2>&1 | tee "$attempt/agent.log"; then
    printf 'Agent failed; artifacts retained in %s\n' "$attempt" >&2
    cleanup; active=''; failed=1; continue
  fi
  active=''
  if [[ ! -f "$attempt/outbox/submission/manifest.json" ]]; then
    printf 'No ready submission for %s\n' "$problem" >&2
    failed=1; continue
  fi
  active="strap-eval-$$-$index-grader"
  if ! container run --rm --progress none --name "$active" --memory 2g --network none \
      --mount "type=bind,source=$attempt/outbox,target=/outbox,readonly" \
      --mount "type=bind,source=$attempt/results,target=/results" \
      --mount "type=bind,source=$out/grading,target=/grading,readonly" \
      strap-eval grade -q 2>&1 | tee "$attempt/grader.log"; then
    printf 'Grader failed; artifacts retained in %s\n' "$attempt" >&2
    cleanup; failed=1
  else
    printf 'Report: %s/results/report.md\n' "$attempt"
  fi
  active=''
done
printf '\nBatch artifacts: %s\n' "$out"
exit "$failed"
