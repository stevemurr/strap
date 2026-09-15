# Booking conflict

The meeting-room service receives a day's bookings for one room as
`[start, end)` pairs in minutes, in the order they were made. Before
publishing the schedule it needs to know whether any two bookings overlap.

## Contract

Package `bookings`, file `bookings.go`:

```go
func HasConflict(bookings [][2]int) bool
```

- Each booking is `[start, end)`: it occupies `start` and every minute up to
  but not including `end`. Every booking has `start < end`; inputs that break
  this never occur.
- Two bookings conflict when they share at least one minute, i.e. each one
  starts strictly before the other ends. A booking that ends exactly when
  another starts does not conflict with it.
- Returns `true` when any pair of bookings conflicts; identical bookings
  conflict with each other. Returns `false` for zero or one booking.
- Times may be negative (bookings carried over from the previous day).
- Bookings arrive in no particular order. The input slice must not be
  modified or reordered; if you need it sorted, sort a copy.

## Examples

| bookings | result |
|---|---|
| `[[0, 30], [5, 10], [15, 20]]` | `true` |
| `[[7, 10], [2, 4]]` | `false` |
| `[[1, 5], [5, 9]]` | `false` |
| `[[2, 6], [2, 6]]` | `true` |
| `[[3, 8]]` | `false` |
| `[]` | `false` |

## Constraints

- Up to 1,000,000 bookings. The call must finish well under a second at that
  size; comparing every pair of bookings is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
