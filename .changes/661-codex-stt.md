### feat(stt): Codex OAuth transcription provider

upstream decolua/9router 421f2262 parity (#625, upstream #5074). `cx/transcribe`
(also `codex/transcribe`) reuses an existing Codex OAuth chat connection instead
of an OpenAI API key. Audio is converted locally to mono 24 kHz PCM16 WAV with
ffmpeg and sent as a file-only multipart request to ChatGPT's internal
`/backend-api/transcribe` endpoint with the Codex identity headers; responses
are limited to `{"text":"..."}` or plain text — language, prompt and temperature
are refused outright, as are unknown or duplicate multipart fields.

Everything the upstream commit bounded is bounded here: 25 MiB input, 120 s
decoded audio, 15 s conversion, 60 s upstream attempt, a 1 MiB / 250k-character
response, and two concurrent transcriptions per process. Private temp files are
removed on both success and failure, and the decoder runs single-threaded
against a whitelisted format list with network protocols disabled. The lazy
token refresh runs first; a 401 earns one forced refresh and a single retry
(the new exported `ChatHandler.ForceRefreshOAuthToken`); client errors end the
request without burning the remaining accounts, while other failures classify
and cool the account down before the fallback loop excludes it. The fixed
endpoint is never taken from a connection URL override, and the connection's
pooled client carries the proxy when one is set — TLS verification is never
relaxed. The registry declares the `stt` kind on codex/cx, so `/v1/models`
keeps `transcribe` out of the chat list.

Verified: new table tests for the WAV builder, the multipart parsing matrix and
the validation matrix, bounded response parsing, and handler-level
upstream-contract tests against a fake server (identity headers, the WAV part,
both response formats, upstream 400 stopping the fallback, an unusable answer
answering 502, missing account data answering 401 without reaching upstream),
plus a real-ffmpeg end-to-end case that skips when ffmpeg is absent. `go test
./...` green apart from the pre-existing environmental browserlogin failure,
the integration suite green, `golangci-lint --new-from-rev` clean. Docs parity
in `docs/CODEX_STT.md` with the ports and transport wording adapted to the Go
gateway.
