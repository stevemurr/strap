package config

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		keys []string
		want string
	}{
		{[]string{"db.host", "db.port", "db.name"}, "db."},
		{[]string{"auth.token", "cache.ttl"}, ""},
		{[]string{"log.level"}, "log.level"},
		{[]string{"app", "apple", "application"}, "app"},
		{[]string{"a", "ab", ""}, ""},
		{nil, ""},
		{[]string{}, ""},
		{[]string{""}, ""},
		{[]string{"", ""}, ""},
		{[]string{"same", "same", "same"}, "same"},
		{[]string{"apple", "app"}, "app"},
		{[]string{"DB.host", "db.host"}, ""},
		{[]string{"flower", "flow", "flight"}, "fl"},
		{[]string{"dog", "racecar", "car"}, ""},
		{[]string{"x.y.z", "x.y.z.w", "x.y"}, "x.y"},
		{[]string{"ab", "ab", "ac"}, "a"},
		{[]string{"abc", "abd", "ab"}, "ab"},
		{[]string{"k", "k"}, "k"},
	}
	for _, c := range cases {
		if got := SharedPrefix(c.keys); got != c.want {
			t.Errorf("SharedPrefix(%q) = %q, want %q", c.keys, got, c.want)
		}
	}
}

func TestHiddenBytewise(t *testing.T) {
	cases := []struct {
		keys []string
		want string
	}{
		{[]string{"café", "cafe"}, "caf"},
		{[]string{"naïve", "naïf"}, "naï"},
		{[]string{"é", "è"}, "\xc3"},
		{[]string{"日本語", "日本"}, "日本"},
	}
	for _, c := range cases {
		if got := SharedPrefix(c.keys); got != c.want {
			t.Errorf("SharedPrefix(%q) = %q, want %q", c.keys, got, c.want)
		}
	}
}

func TestHiddenDoesNotMutate(t *testing.T) {
	keys := []string{"zeta.b", "alpha.c", "zeta.a", "alpha.b"}
	before := append([]string(nil), keys...)
	SharedPrefix(keys)
	if !reflect.DeepEqual(keys, before) {
		t.Fatalf("input modified: %q", keys)
	}
}

func hiddenBrute(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	for n := len(keys[0]); n >= 0; n-- {
		candidate := keys[0][:n]
		all := true
		for _, k := range keys {
			if !strings.HasPrefix(k, candidate) {
				all = false
				break
			}
		}
		if all {
			return candidate
		}
	}
	return ""
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(14))
	alphabet := []byte("ab\xc3")
	for round := 0; round < 3000; round++ {
		keys := make([]string, rng.Intn(6))
		for i := range keys {
			buf := make([]byte, rng.Intn(5))
			for j := range buf {
				buf[j] = alphabet[rng.Intn(len(alphabet))]
			}
			keys[i] = string(buf)
		}
		if got, want := SharedPrefix(keys), hiddenBrute(keys); got != want {
			t.Fatalf("round %d: SharedPrefix(%q) = %q, want %q", round, keys, got, want)
		}
	}
}

func TestHiddenLargeGroup(t *testing.T) {
	const n = 200_000
	const width = 50
	base := strings.Repeat("k", width-1)
	keys := make([]string, n)
	for i := range keys {
		keys[i] = base + string(rune('a'+i%26))
	}
	same := make([]string, n)
	for i := range same {
		same[i] = base + "z"
	}
	split := append([]string(nil), keys...)
	split[n/2] = base[:10] + "X" + base[11:] + "a"
	run := func(name string, keys []string, want string) {
		done := make(chan string, 1)
		go func() { done <- SharedPrefix(keys) }()
		select {
		case got := <-done:
			if got != want {
				t.Fatalf("%s: SharedPrefix on %d keys returned %d bytes, want %d", name, n, len(got), len(want))
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: SharedPrefix took longer than 10s on %d keys", name, n)
		}
	}
	run("last byte differs", keys, base)
	run("identical", same, base+"z")
	run("one odd key", split, base[:10])
}
