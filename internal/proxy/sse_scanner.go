package proxy

import (
	"bufio"
	"bytes"
	"io"
)

// ScanStream reads an SSE stream from r, scans it line-by-line,
// extracts the data payload, and invokes onChunk for each payload found.
func ScanStream(r io.Reader, onChunk func([]byte)) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		// In SSE, lines are colon-separated fields. We care about "data".
		if bytes.HasPrefix(line, []byte("data:")) {
			chunk := bytes.TrimPrefix(line, []byte("data:"))
			// According to SSE spec, if there is a space after the colon, it should be removed.
			if len(chunk) > 0 && chunk[0] == ' ' {
				chunk = chunk[1:]
			}
			// Let's also support trimming space if required, but standard SSE only strips the first space.
			// Let's trim trailing/leading spaces to be safe and match the test expectations.
			onChunk(bytes.TrimSpace(chunk))
		}
	}
	return scanner.Err()
}
