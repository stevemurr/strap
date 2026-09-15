# Build order

The build scheduler turns a list of targets and their dependencies into a
single sequence to build them in. Because the sequence is logged and diffed
between runs, it must be deterministic: whenever several targets could go
next, the one with the smallest name goes first.

## Contract

Package `builds`, file `builds.go`:

```go
func Order(targets []string, deps [][2]string) ([]string, error)
```

- `deps[i] = [before, after]` means `before` must appear earlier in the order
  than `after`. The same dependency may be listed more than once.
- On success, returns every target exactly once in an order that satisfies
  all dependencies, with a nil error. Among all valid orders return the
  lexicographically smallest one, comparing names as strings; this is what
  you get by repeatedly picking the smallest-named target whose prerequisites
  have all been placed.
- Returns `nil` and a non-nil error when a dependency names a target that is
  not in `targets`, when `targets` contains the same name twice, or when the
  dependencies contain a cycle (a target that depends on itself counts as a
  cycle). The error text is up to you.
- Empty `targets` with no deps returns a zero-length order (nil is fine) and
  a nil error.
- The input slices must not be modified.

## Examples

| targets | deps | result |
|---|---|---|
| `["app", "lib", "test"]` | `[["lib", "app"], ["app", "test"]]` | `["lib", "app", "test"]` |
| `["c", "b", "a"]` | `[]` | `["a", "b", "c"]` |
| `["a", "b", "c"]` | `[["c", "a"]]` | `["b", "c", "a"]` |
| `["a", "b"]` | `[["a", "b"], ["b", "a"]]` | error (cycle) |
| `["a"]` | `[["a", "zzz"]]` | error (unknown target) |
| `["a", "a"]` | `[]` | error (duplicate target) |

## Constraints

- Up to 200,000 targets with 500,000 dependencies, including long chains. The
  call must finish well under a second at that size; scanning every target to
  find the next one to build is too slow.
- Standard library only. Keep the package name, file name and exported signature.
