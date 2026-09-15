// Package autocomplete indexes known search terms for prefix suggestions.
package autocomplete

// Index stores distinct terms and answers exact and prefix queries.
type Index struct{}

// New returns an empty index.
func New() *Index {
	panic("not implemented")
}

// Add stores term; storing a term that is already present changes nothing.
func (x *Index) Add(term string) {
	panic("not implemented")
}

// Contains reports whether exactly term has been stored.
func (x *Index) Contains(term string) bool {
	panic("not implemented")
}

// HasPrefix reports whether at least one stored term starts with prefix.
func (x *Index) HasPrefix(prefix string) bool {
	panic("not implemented")
}

// CountPrefix returns how many distinct stored terms start with prefix.
func (x *Index) CountPrefix(prefix string) int {
	panic("not implemented")
}
