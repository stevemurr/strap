// Package collation reconstructs a partner's alphabet ordering from word lists
// sorted under it.
package collation

import "errors"

// InferOrder compares each adjacent pair of words. The first position where
// they differ yields one precedence edge between two letters; running out of
// letters on the longer word first is a prefix violation. Kahn's algorithm
// then repeatedly emits the a-z smallest letter with no pending
// predecessors, which produces the lexicographically smallest topological
// order; if letters remain when nothing is ready, the constraints form a
// cycle.
func InferOrder(sorted []string) (string, error) {
	var present [26]bool
	var edge [26][26]bool
	var pending [26]int
	letters := 0
	for _, w := range sorted {
		for i := 0; i < len(w); i++ {
			if l := w[i] - 'a'; !present[l] {
				present[l] = true
				letters++
			}
		}
	}
	for i := 1; i < len(sorted); i++ {
		a, b := sorted[i-1], sorted[i]
		j := 0
		for j < len(a) && j < len(b) && a[j] == b[j] {
			j++
		}
		if j == len(a) || j == len(b) {
			if len(a) > len(b) {
				return "", errors.New("collation: a word precedes its own prefix")
			}
			continue
		}
		x, y := a[j]-'a', b[j]-'a'
		if !edge[x][y] {
			edge[x][y] = true
			pending[y]++
		}
	}
	out := make([]byte, 0, letters)
	var emitted [26]bool
	for len(out) < letters {
		pick := -1
		for l := 0; l < 26; l++ {
			if present[l] && !emitted[l] && pending[l] == 0 {
				pick = l
				break
			}
		}
		if pick < 0 {
			return "", errors.New("collation: the constraints contradict each other")
		}
		emitted[pick] = true
		out = append(out, byte('a'+pick))
		for m := 0; m < 26; m++ {
			if edge[pick][m] {
				pending[m]--
			}
		}
	}
	return string(out), nil
}
