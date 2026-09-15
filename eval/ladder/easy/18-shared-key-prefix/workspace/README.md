# Shared key prefix

The configuration service groups settings by the prefix their keys share, so
that `db.host`, `db.port` and `db.name` collapse into a `db.` group in the
admin UI. Given the keys of one group, compute the longest prefix they all
share.

## Contract

Package `config`, file `prefix.go`:

```go
func SharedPrefix(keys []string) string
```

- Returns the longest string that is a prefix of every key. Comparison is
  byte by byte, so the result may end in the middle of a multi-byte UTF-8
  character; that is intended.
- An empty slice returns `""`. If any key is `""`, the result is `""`.
- A single key returns that key unchanged.
- Keys are case-sensitive: `"DB.host"` and `"db.host"` share no prefix.
- The input slice must not be modified or reordered.

## Examples

| keys | result |
|---|---|
| `["db.host", "db.port", "db.name"]` | `"db."` |
| `["auth.token", "cache.ttl"]` | `""` |
| `["log.level"]` | `"log.level"` |
| `["app", "apple", "application"]` | `"app"` |
| `["a", "ab", ""]` | `""` |
| `[]` | `""` |

## Constraints

- Up to 200,000 keys of up to 50 bytes each. The call must finish well under
  a second at that size; touching each byte of each key a handful of times is
  fine, while comparing every pair of keys or repeatedly rebuilding strings is
  far too slow.
- Standard library only. Keep the package name, file name and exported signature.
