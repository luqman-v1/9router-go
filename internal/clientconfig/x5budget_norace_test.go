//go:build !race

package clientconfig

import "time"

// x5Budget is the §7.4 X5 ceiling for a 64 KB file. The race detector slows
// every memory access by roughly an order of magnitude, so the assertion would
// measure instrumentation, not the patcher; x5budget_race_test.go relaxes it.
//
// There is deliberately no second wall-clock budget for the scaling test:
// TestPatchScalesLinearly compares two sizes in one process, and a ratio is the
// shape of the cost rather than the size of the box, so it needs no ceiling
// that a busy CI machine could invalidate.
const x5Budget = 200 * time.Millisecond
