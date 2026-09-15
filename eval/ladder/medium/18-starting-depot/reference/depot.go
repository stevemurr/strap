// Package route picks the starting depot for the circular delivery route.
package route

// StartingDepot walks the route once. Whenever the tank would go negative,
// no depot between the current candidate and here can be a start (they all
// reach this leg with even less fuel), so the candidate moves past it. If the
// total surplus is non-negative the final candidate completes the loop, and
// it is the smallest feasible index because every earlier depot was ruled out.
func StartingDepot(fuel, cost []int) int {
	start, tank, total := 0, 0, 0
	for i := range fuel {
		d := fuel[i] - cost[i]
		total += d
		tank += d
		if tank < 0 {
			start, tank = i+1, 0
		}
	}
	if total < 0 || start >= len(fuel) {
		return -1
	}
	return start
}
