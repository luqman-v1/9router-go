//go:build linux

package tray

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// init installs the Linux L2 lookup: the only platform in x/sys where "which
// process holds this port" is answerable without privileges.
func init() {
	socketVerified = verifySocketOwner
}

// verifySocketOwner proves the process listening on the configured port is the
// same one the pid file claims, and that the claim's executable matches.
//
// Two layers, both required (§C2):
//
//	L1 — the pid file names a live process running the recorded executable;
//	     daemon.RunningPID() already applies that and returns 0 otherwise.
//	L2 — that PID is the one holding the listening socket, read from the kernel
//	     rather than inferred from the port answering /health.
//
// /health cannot be the oracle: it is a constant any listener can reproduce
// (§5.1 of the tray spec).
func verifySocketOwner() bool {
	pid := daemonPID()
	if pid <= 0 {
		return false
	}
	inode, ok := listenerInode(portFromConfig())
	if !ok {
		return false
	}
	holder, ok := pidHoldingSocket(inode)
	if !ok {
		return false
	}
	return holder == pid
}

// listenerInode finds the socket inode for a TCP listener on port.
// /proc/net/tcp and tcp6 share a format; the port is the hex half of
// local_address, and state 0A is LISTEN.
func listenerInode(port int) (string, bool) {
	want := strings.ToUpper(strconv.FormatInt(int64(port), 16))
	if len(want)%2 == 1 {
		want = "0" + want
	}
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if inode, ok := scanProcNetTCP(name, want); ok {
			return inode, true
		}
	}
	return "", false
}

func scanProcNetTCP(path, wantPortHex string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	// Read-only file: a close error cannot lose data, and the caller's question
	// ("who owns this socket") is already answered by the bytes read above.
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Scan() // header
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// sl, local_address, rem_address, st, ..., inode at index 9
		if len(fields) < 10 {
			continue
		}
		if fields[3] != "0A" { // LISTEN
			continue
		}
		addr := fields[1]
		colon := strings.LastIndexByte(addr, ':')
		if colon < 0 || addr[colon+1:] != wantPortHex {
			continue
		}
		return fields[9], true
	}
	return "", false
}

// pidHoldingSocket scans /proc/<pid>/fd for the socket inode. Every process is
// inspected: a listener may belong to a supervisor's child, so assuming our own
// PID would prove nothing.
func pidHoldingSocket(inode string) (int, bool) {
	target := "socket:[" + inode + "]"
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid <= 0 {
			continue
		}
		fdDir := filepath.Join("/proc", d.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // process exited, or not ours to inspect
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if link == target {
				return pid, true
			}
		}
	}
	return 0, false
}
