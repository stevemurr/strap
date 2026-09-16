package eval

import (
	"testing"
	"time"
)

func TestRunNameGroupsByCommitThenProfile(t *testing.T) {
	at := time.Date(2026, 9, 16, 11, 3, 2, 0, time.Local)
	if got := RunName("1fd9a4c", "qwen3.8-flash-next-thinking", at); got != "1fd9a4c_qwen3.8-flash-next-thinking_20260916-110302" {
		t.Fatal(got)
	}
	if got := RunName("1fd9a4c-dirty", "org/model name:v2", at); got != "1fd9a4c-dirty_org-model-name-v2_20260916-110302" {
		t.Fatal(got)
	}
	if got := RunName("", "", at); got != "unknown_unknown_20260916-110302" {
		t.Fatal(got)
	}
}

func TestCommitFromBuildSettings(t *testing.T) {
	cases := []struct {
		settings map[string]string
		want     string
	}{
		{map[string]string{}, "nogit"},
		{map[string]string{"vcs.revision": "1fd9a4c0123456789", "vcs.modified": "false"}, "1fd9a4c"},
		{map[string]string{"vcs.revision": "1fd9a4c0123456789", "vcs.modified": "true"}, "1fd9a4c-dirty"},
		{map[string]string{"vcs.revision": "abc"}, "abc"},
	}
	for _, c := range cases {
		if got := commitFromSettings(c.settings); got != c.want {
			t.Fatalf("%v: got %q want %q", c.settings, got, c.want)
		}
	}
}
