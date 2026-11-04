//go:build !windows

package mcodelease

import (
	"context"
	"net"
	"time"
)

// dialWindowsPipe is unreachable off Windows: ResolveEndpoint only returns a
// pipe transport on Windows. It exists so the package compiles per target.
func dialWindowsPipe(ctx context.Context, address string, timeout time.Duration) (net.Conn, error) {
	return nil, leaseError(CodeInvalidRequest, "named pipes are a Windows transport")
}

func isWindows() bool { return false }
