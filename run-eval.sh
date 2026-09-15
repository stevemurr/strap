#!/bin/sh
set -eu

# Runs the whole ladder for one model profile, one tier at a time, into
# DIRECTORY, then writes the report. Works from any working directory: the
# ladder is located relative to this script, and DIRECTORY is resolved
# relative to where you run the script.
main() {
	if [ "$#" -ne 2 ] || [ -z "$1" ] || [ -z "$2" ]; then
		printf 'Usage: %s PROFILE DIRECTORY\n' "$0" >&2
		return 2
	fi

	profile=$1
	directory=$2
	script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
	ladder=$script_dir/eval/ladder

	if [ ! -d "$ladder" ]; then
		printf '%s: ladder not found at %s\n' "$0" "$ladder" >&2
		return 1
	fi

	for tier in easy medium hard; do
		strap-eval run -ladder "$ladder" -tier "$tier" -parallel 2 -profile "$profile" -out "$directory"
	done
	strap-eval report "$directory"
}

main "$@"
