// Package autocomplete indexes known search terms for prefix suggestions.
package autocomplete

// node is one trie node. count is the number of distinct stored terms that
// pass through or end at this node, so a prefix query is a single walk.
type node struct {
	children [26]*node
	terminal bool
	count    int
}

// Index stores distinct terms in a trie and answers exact and prefix queries.
type Index struct {
	root node
}

// New returns an empty index.
func New() *Index {
	return &Index{}
}

// Add stores term; storing a term that is already present changes nothing.
func (x *Index) Add(term string) {
	if x.Contains(term) {
		return
	}
	n := &x.root
	n.count++
	for i := 0; i < len(term); i++ {
		c := term[i] - 'a'
		if n.children[c] == nil {
			n.children[c] = &node{}
		}
		n = n.children[c]
		n.count++
	}
	n.terminal = true
}

// walk returns the node reached by following s from the root, or nil.
func (x *Index) walk(s string) *node {
	n := &x.root
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return nil
		}
		n = n.children[s[i]-'a']
		if n == nil {
			return nil
		}
	}
	return n
}

// Contains reports whether exactly term has been stored.
func (x *Index) Contains(term string) bool {
	n := x.walk(term)
	return n != nil && n.terminal
}

// HasPrefix reports whether at least one stored term starts with prefix.
func (x *Index) HasPrefix(prefix string) bool {
	return x.CountPrefix(prefix) > 0
}

// CountPrefix returns how many distinct stored terms start with prefix.
func (x *Index) CountPrefix(prefix string) int {
	n := x.walk(prefix)
	if n == nil {
		return 0
	}
	return n.count
}
