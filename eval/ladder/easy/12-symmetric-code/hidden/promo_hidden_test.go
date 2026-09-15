package promo

import (
	"math/rand"
	"testing"
	"time"
)

func TestHiddenExamples(t *testing.T) {
	cases := []struct {
		code string
		want bool
	}{
		{"A man, a plan, a canal: Panama", true},
		{"race a car", false},
		{"12-21", true},
		{"0P", false},
		{"!!!", true},
		{"", true},
		{"a", true},
		{"Z", true},
		{"7", true},
		{" ", true},
		{"ab", false},
		{"aA", true},
		{"Ab-Ba", true},
		{"No 'x' in Nixon", true},
		{"Was it a car or a cat I saw?", true},
		{"1a2", false},
		{"a1a", true},
		{"a.", true},
		{".a.b", false},
		{"1221", true},
		{"12 3 21", true},
		{"abcba!", true},
		{"abcbaX", false},
	}
	for _, c := range cases {
		if got := IsSymmetric(c.code); got != c.want {
			t.Errorf("IsSymmetric(%q) = %v, want %v", c.code, got, c.want)
		}
	}
}

func TestHiddenNonASCIIBytesAreIgnored(t *testing.T) {
	cases := []struct {
		code string
		want bool
	}{
		{"é", true},
		{"aéa", true},
		{"aéb", false},
		{"日本", true},
		{"日a本b", false},
		{"\xff1\xfe1\xff", true},
	}
	for _, c := range cases {
		if got := IsSymmetric(c.code); got != c.want {
			t.Errorf("IsSymmetric(%q) = %v, want %v", c.code, got, c.want)
		}
	}
}

func brute(code string) bool {
	var kept []byte
	for i := 0; i < len(code); i++ {
		b := code[i]
		switch {
		case b >= 'a' && b <= 'z', b >= '0' && b <= '9':
			kept = append(kept, b)
		case b >= 'A' && b <= 'Z':
			kept = append(kept, b+('a'-'A'))
		}
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		if kept[i] != kept[j] {
			return false
		}
	}
	return true
}

func TestHiddenAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(125))
	alphabet := []byte("aAbB01 ,.-\xc3\xa9")
	for round := 0; round < 2000; round++ {
		n := rng.Intn(12)
		buf := make([]byte, n)
		for i := range buf {
			buf[i] = alphabet[rng.Intn(len(alphabet))]
		}
		// Every other round mirrors the first half, flipping letter case at
		// random, so symmetric codes are as common as asymmetric ones.
		if round%2 == 0 {
			for i := 0; i < n/2; i++ {
				c := buf[i]
				if rng.Intn(2) == 0 {
					switch {
					case c >= 'a' && c <= 'z':
						c -= 'a' - 'A'
					case c >= 'A' && c <= 'Z':
						c += 'a' - 'A'
					}
				}
				buf[n-1-i] = c
			}
		}
		code := string(buf)
		if got, want := IsSymmetric(code), brute(code); got != want {
			t.Fatalf("round %d: IsSymmetric(%q) = %v, want %v", round, code, got, want)
		}
	}
}

func TestHiddenLongCode(t *testing.T) {
	const n = 1_000_000
	half := make([]byte, (n-2)/2)
	for i := range half {
		switch {
		case i%9 == 0:
			half[i] = ' '
		case i%2 == 0:
			half[i] = byte('A' + i%26)
		default:
			half[i] = byte('0' + i%10)
		}
	}
	mirror := make([]byte, 0, n)
	mirror = append(mirror, half...)
	for i := len(half) - 1; i >= 0; i-- {
		b := half[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		mirror = append(mirror, b)
	}
	symmetric := string(mirror)
	// Two different letters in the middle break the symmetry and nothing else.
	broken := symmetric[:len(half)] + "xy" + symmetric[len(half):]
	run := func(name, code string, want bool) {
		done := make(chan bool, 1)
		go func() { done <- IsSymmetric(code) }()
		select {
		case got := <-done:
			if got != want {
				t.Fatalf("%s: IsSymmetric on %d bytes = %v, want %v", name, len(code), got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: IsSymmetric took longer than 10s on %d bytes", name, len(code))
		}
	}
	run("symmetric", symmetric, true)
	run("broken", broken, false)
}
