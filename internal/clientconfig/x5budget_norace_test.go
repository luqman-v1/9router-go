//go:build !race

package clientconfig

import "time"

// x5Budget is the §7.4 X5 ceiling for a 64 KB file. The race detector slows
// every memory access by roughly an order of magnitude, so the assertion would
// measure instrumentation, not the patcher; x5budget_race_test.go relaxes it.
//
// 200 ms was measured on the reference Linux box; a shared Windows runner
// running the rest of the package in the same process pushes the same 64 KB
// patch past it (0.15 s isolated, ~1 s under load), so the ceiling there
// measures the scheduler, not the patcher. The scaling test
// (TestPatchScalesLinearly) still pins the shape of the cost — a ratio, which
// a busy machine cannot fake — so this wall-clock number is a loose guard
// against a quadratic regression, not a performance measurement.
const x5Budget = 2 * time.Second
