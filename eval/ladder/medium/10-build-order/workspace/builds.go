// Package builds schedules build targets so that every dependency is built
// before the targets that need it.
package builds

// Order returns the lexicographically smallest sequence of targets that
// satisfies every dependency, or an error for unknown targets, duplicate
// targets or cycles. README.md defines the tie-break, the error cases and the
// performance requirement.
func Order(targets []string, deps [][2]string) ([]string, error) {
	panic("not implemented")
}
