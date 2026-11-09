package clientconfig

import "strings"

// The /v1 rule from docs/specs/client-schema-matrix.md §7.1. Eight of the nine
// clients want the gateway origin with "/v1" on the end; cline wants the bare
// origin. Getting this backwards produces a client that authenticates and then
// 404s every request, which is the failure the matrix calls out first.

// withV1 appends /v1 when the URL does not already end with it.
//
// Only the exact tail counts. "https://gw/v1beta" ends with neither, so it gets
// /v1 — the gateway has no such path, but guessing at a version suffix the
// caller did not write would be inventing one, and the caller who wrote /v1beta
// meant something.
func withV1(base string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(b, "/v1") {
		return b
	}
	return b + "/v1"
}

// originOnly strips a trailing /v1 so the URL is the bare origin root.
//
// Stripping only the version suffix keeps any path prefix the operator deployed
// under: "https://host/gw/v1" becomes "https://host/gw", which is the base a
// cline deployment sitting behind a path needs.
func originOnly(base string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	return strings.TrimSuffix(b, "/v1")
}
