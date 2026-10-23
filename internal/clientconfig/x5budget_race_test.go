//go:build race

package clientconfig

import "time"

// x5Budget under the race detector. See x5budget_norace_test.go.
const x5Budget = 30 * time.Second
