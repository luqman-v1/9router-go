//go:build !windows

package clientconfig

// isRetryableRenameErr is Windows-only. POSIX rename fails for reasons that do
// not clear on their own — EACCES, ENOSPC, EXDEV — so retrying would only delay
// the error the caller needs.
func isRetryableRenameErr(err error) bool { return false }
