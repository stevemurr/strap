package eval

import (
	"runtime/debug"
	"strings"
	"time"
)

// BuildCommit returns the short VCS revision compiled into the binary, with a
// -dirty suffix when the tree had uncommitted changes, or "nogit" when the
// build carried no VCS information (a go run, or a build outside git).
func BuildCommit() string {
	settings := map[string]string{}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			settings[s.Key] = s.Value
		}
	}
	return commitFromSettings(settings)
}

func commitFromSettings(settings map[string]string) string {
	revision := settings["vcs.revision"]
	if revision == "" {
		return "nogit"
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if settings["vcs.modified"] == "true" {
		revision += "-dirty"
	}
	return revision
}

// RunName names a run directory <commit>_<profile>_<timestamp>, so runs group
// by the harness build first and then by model configuration; the timestamp
// keeps two runs of the same pair apart.
func RunName(commit, profile string, at time.Time) string {
	return sanitizeName(commit) + "_" + sanitizeName(profile) + "_" + at.Format("20060102-150405")
}

// sanitizeName keeps letters, digits, dots and dashes; every other run of
// characters becomes one dash, and underscores are reserved as the separator.
func sanitizeName(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unknown"
	}
	return out
}
