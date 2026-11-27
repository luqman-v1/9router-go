### SSRF guard on UC Web input image downloads

- Routed remote `http://` and `https://` input image fetches in `ucResolveImageBytes` through `visionImageDownloadClient()` (`proxy.NewStrictSafeTransport` plus `checkRedirectPublic`) instead of `ucHTTPClient`, blocking loopback, private network, and cloud metadata destinations including redirects.
- Verified with `TestUcResolveImageBytes_RefusesPrivateLoopbackAndRedirects` in `internal/handlers/media/uc_web_test.go` (`go test -count=1 ./internal/handlers/media/...`, `go vet ./internal/handlers/media/...`).