//go:build windows

package clientconfig

// fsyncDir is a no-op on Windows: opening a directory for sync fails with
// ERROR_ACCESS_DENIED there, and Windows already commits the rename through
// NtSetInformationFile. Reporting that failure would be noise on every write.
func fsyncDir(r rootFS, dir string) {}
