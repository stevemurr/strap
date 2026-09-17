package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stevemurr/strap/tool"
)

// Decode only the built-in result envelopes. The recorded result is unchanged;
// unfamiliar or malformed results remain literal text in the view.
func (d *toolDisplay) nativeOutput(raw string) {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return
	}
	switch d.name {
	case "shell":
		var result tool.ShellResult
		if fields["output"] == nil || fields["started"] == nil || json.Unmarshal([]byte(raw), &result) != nil {
			return
		}
		d.result = boundedToolText(result.Output, 32768)
		var failures []string
		if result.ExitCode != nil && *result.ExitCode != 0 {
			failures = append(failures, fmt.Sprintf("exit %d", *result.ExitCode))
		}
		if result.TimedOut {
			failures = append(failures, "timed out")
		}
		if result.Cancelled {
			failures = append(failures, "cancelled")
		}
		for _, err := range []string{result.StartError, result.WaitError, result.CleanupError} {
			if err != "" {
				failures = append(failures, err)
			}
		}
		d.failure = boundedToolText(strings.Join(failures, " · "), 2048)
		if result.Truncated {
			d.notice = "Output truncated by shell"
		}
		if result.OutputIncomplete {
			d.notice = strings.TrimPrefix(d.notice+" · Output stream incomplete", " · ")
		}
	case "read_file":
		var result tool.ReadFileResult
		if fields["content"] == nil || json.Unmarshal([]byte(raw), &result) != nil {
			return
		}
		d.result = boundedToolText(result.Content, 32768)
		if result.More || result.Truncated {
			d.notice = "Partial file view"
		}
	case "write_file":
		var result tool.WriteFileResult
		if fields["bytes_written"] != nil && json.Unmarshal([]byte(raw), &result) == nil {
			d.result = fmt.Sprintf("Wrote %d bytes", result.BytesWritten)
		}
	case "edit_file":
		var result tool.EditFileResult
		if fields["replacements"] != nil && json.Unmarshal([]byte(raw), &result) == nil {
			d.result = fmt.Sprintf("%d replacement(s)", result.Replacements)
		}
	}
}
