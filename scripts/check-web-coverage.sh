#!/bin/sh
# Run offline web tests and require every compiled statement in the web path.
set -eu
cd "$(dirname "$0")/.."

profile=$(mktemp "${TMPDIR:-/tmp}/strap-web-coverage.XXXXXX")
trap 'rm -f "$profile"' EXIT
trap 'exit 1' HUP INT TERM

STRAP_LIVE_WEB=0 go test -race -count=1 -coverprofile="$profile" -timeout=90s \
    ./tool ./internal/agentbrowser ./internal/webkit ./internal/webprocess

awk '
NR == 1 { next }
{
    split($1, location, ":")
    file = location[1]
    sub(/^github.com\/stevemurr\/strap\//, "", file)
    if (file !~ /^tool\/(web|web_search|open_url)\.go$/ &&
        file !~ /^internal\/(agentbrowser|webkit|webprocess)\//) next
    if (!(file in total)) files[++count] = file
    total[file] += $2
    covered[file] += ($3 > 0 ? $2 : 0)
    if ($3 == 0) {
        print "Uncovered: " $1 > "/dev/stderr"
        failed = 1
    }
    if (file ~ /^internal\/agentbrowser\//) browser = 1
    if (file ~ /^internal\/webkit\//) webkit = 1
    if (file ~ /^internal\/webprocess\//) process = 1
}
END {
    if (!("tool/web.go" in total) || !("tool/web_search.go" in total) ||
        !("tool/open_url.go" in total) || !browser || !webkit || !process) {
        print "Coverage profile is missing part of the web path" > "/dev/stderr"
        failed = 1
    }
    for (i = 1; i <= count; i++) {
        file = files[i]
        printf "%s: %d/%d statements (%.2f%%)\n", file, covered[file], total[file], 100 * covered[file] / total[file]
    }
    exit failed
}' "$profile"
