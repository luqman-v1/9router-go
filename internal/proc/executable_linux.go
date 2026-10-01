//go:build linux

package proc

import "os"

// /proc/<pid>/exe is a symlink to the running image and resolves to the same
// path os.Executable reports for this process, so no platform-specific
// comparison is needed here.
func executablePath(pid int) (string, error) {
	path, err := os.Readlink("/proc/" + itoa(pid) + "/exe")
	if err != nil {
		return "", err
	}
	return path, nil
}

func itoa(pid int) string {
	if pid == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for pid > 0 {
		i--
		buf[i] = byte('0' + pid%10)
		pid /= 10
	}
	return string(buf[i:])
}