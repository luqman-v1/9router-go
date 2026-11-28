### Media handlers: split by endpoint family, and check the closes they now own

`internal/handlers/media/media.go` was 2445 lines owning several distinct endpoint families. Each now lives in its own file in the same package, with the same contracts: `media_embeddings.go`, `media_audio.go`, `media_systemone.go`, `media_image_video.go`, `media_web.go`, `media_forward.go`, `media_forward_conn.go`, and `media.go` for the handler struct, connection selection and the JSON media lane.

The split made every line in those files new to the lint ratchet, which surfaced unchecked returns that had been hiding in one long file:

- `defer r.Body.Close()` / `defer resp.Body.Close()` → `defer func() { _ = r.Body.Close() }()` across the moved handlers, and `_ = resp.Body.Close()` for the non-deferred close in the upstream-error branch.
- `io.Copy(w, resp.Body)` in `media_forward.go` now records a mid-stream failure instead of dropping it: the status line is already on the wire, so the only thing left to do is log it.
- `UpdateConnectionLastUsed` is written as an explicit best-effort call, so a bookkeeping failure cannot turn a served 200 into an error.
- `err == io.EOF` → `errors.Is(err, io.EOF)` in the multipart rewrite.

Verified with `golangci-lint run --new-from-rev=origin/main ./internal/handlers/media/...` (0 issues) and `go test ./internal/handlers/media/`.
