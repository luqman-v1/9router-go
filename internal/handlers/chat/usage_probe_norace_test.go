//go:build !race

package chat

// raceDetectorEnabled reports whether this test binary was built with -race.
// See usage_probe_race_test.go for why the latency scenarios care.
const raceDetectorEnabled = false
