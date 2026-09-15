# Largest free block

The event-seating tool shows a venue as a grid of seats, some of them already
taken. For group bookings the organizers want to know the largest rectangular
block of free seats that is still available.

## Contract

Package `seating`, file `block.go`:

```go
func LargestFreeBlock(grid []string) int
```

- Each string is one row of seats; all rows have the same length. `'.'` marks
  a free seat and `'#'` a taken seat; no other characters appear.
- A block is an axis-aligned rectangle of consecutive rows and consecutive
  columns in which every seat is free. Return the area (rows times columns) of
  the largest block.
- Returns `0` when the grid has no rows, when its rows are empty strings, or
  when no seat is free.

## Examples

| grid | result |
|---|---|
| `[".#.##", ".#...", ".....", ".##.#"]` | 6 |
| `["..", ".."]` | 4 |
| `["...", ".#.", "..."]` | 3 |
| `[".#", "#."]` | 1 |
| `["#"]` | 0 |
| `[]` | 0 |

The first grid's largest block is the 2 x 3 block in the middle of rows 2 and 3
(columns 3 to 5).

## Constraints

- Up to 1,000 rows and 1,000 columns. The call must finish well under a second
  at that size; checking every candidate rectangle, or growing a rectangle from
  every seat, is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
