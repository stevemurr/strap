package work

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"sync"
)

// SeededIDs issues ids that are a pure function of seed and issue order: the
// nth id with a prefix is derived from HMAC-SHA256(seed, prefix, n). A session
// with the same seed issues the same ids in the same order, so a recorded run
// replays exactly, yet the ids stay opaque: without the seed a model cannot
// extrapolate the next one.
func SeededIDs(seed []byte) func(prefix string) string {
	var mu sync.Mutex
	counts := map[string]uint64{}
	key := append([]byte(nil), seed...)
	return func(prefix string) string {
		mu.Lock()
		n := counts[prefix]
		counts[prefix]++
		mu.Unlock()
		m := hmac.New(sha256.New, key)
		m.Write([]byte(prefix))
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], n)
		m.Write(b[:])
		suffix := strconv.FormatUint(binary.BigEndian.Uint64(m.Sum(nil))%idSpace, 36)
		for len(suffix) < idLength {
			suffix = "0" + suffix
		}
		return prefix + suffix
	}
}

// DeriveKey derives a named secret from a session seed, so everything the
// engine would otherwise draw at random is reproducible from one value.
func DeriveKey(seed []byte, name string) [32]byte {
	m := hmac.New(sha256.New, seed)
	m.Write([]byte(name))
	var out [32]byte
	copy(out[:], m.Sum(nil))
	return out
}
