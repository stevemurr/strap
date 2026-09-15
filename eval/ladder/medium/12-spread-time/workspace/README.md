# Spread time

The lab's device rack is modelled as a grid. Some machines are already
infected by a worm, some are susceptible, and some slots are empty. Every
minute, each infected machine infects every susceptible machine directly
above, below, left or right of it. Operations wants to know how long it takes
until the whole rack is compromised, or whether part of it stays safe for good.

## Contract

Package `outbreak`, file `spread.go`:

```go
func SpreadTime(grid []string) int
```

- `grid` holds the rows top to bottom; all rows have the same length. Each byte
  is `'I'` (infected), `'S'` (susceptible) or `'.'` (empty slot).
- During minute 1 every susceptible machine orthogonally adjacent to an
  initially infected machine becomes infected. During minute 2 every
  susceptible machine adjacent to any infected machine (including those
  infected in minute 1) becomes infected, and so on. Only the four orthogonal
  neighbours count; diagonals and empty slots do not transmit.
- Returns the number of minutes after which no susceptible machine remains.
- Returns `0` when there is no susceptible machine to begin with, including an
  empty grid (no rows, or rows of length zero).
- Returns `-1` when at least one susceptible machine can never be infected,
  for example because there is no infected machine at all or the machine is
  walled off by empty slots.
- The input must not be modified.

## Examples

| grid | result |
|---|---|
| `["ISS", "SS.", ".SS"]` | `4` |
| `["ISS", ".SS", "S.S"]` | `-1` |
| `[".I"]` | `0` |
| `["ISSSI"]` | `2` |
| `["SSS"]` | `-1` |
| `[]` | `0` |

## Constraints

- Grids up to 1,000 x 1,000. The hidden tests include a winding corridor whose
  far end is about 500,000 minutes away; the call must finish well under a
  second, so re-scanning the whole grid once per minute is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
