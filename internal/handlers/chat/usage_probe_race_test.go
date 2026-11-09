//go:build race

package chat

// raceDetectorEnabled reports whether this test binary was built with -race.
//
// The latency probe measures wall-clock cost, and the race detector slows every
// memory access by roughly an order of magnitude: the same daily-blob
// read-modify-write measured 18.6 ms/request plain and 152 ms/request under
// -race on this tree. A latency number produced under the detector therefore
// describes instrumentation, not the write path, so those scenarios skip
// themselves there. internal/clientconfig/x5budget_race_test.go relaxes its
// budget for the same reason.
const raceDetectorEnabled = true
