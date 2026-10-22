//go:build !race

package clientconfig

import "time"

// x5Budget is the §7.4 X5 ceiling for a 64 KB file. The race detector slows
// every memory access by roughly an order of magnitude, so the assertion would
// measure instrumentation, not the patcher; x5budget_race_test.go relaxes it.
const x5Budget = 200 * time.Millisecond

// scaleBudget is the TestPatchStaysLinear ceiling for a 512 KB sample. Same
// reasoning as x5Budget: the race detector's instrumentation is what the number
// would measure, so scalerace_test.go relaxes it.
const scaleBudget = 250 * time.Millisecond
