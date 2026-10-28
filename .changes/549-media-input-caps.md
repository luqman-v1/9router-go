### feat(media): cap the media a single request may carry, and report what it carried

The transport ceiling (`middleware.MediaMaxBodySize`, 128MB) protects the
process. It does not protect the bill: a request that passes it is still
dispatched upstream, and the upstream call is what gets billed. Every media
lane accepted that body without measuring it, so a caller could send an
arbitrary number of media items of an arbitrary size and the cost was the
provider's problem.

- **Caps, refused before any upstream call.** At most 8 media items per
  request, and at most 25MB for any single item that is not audio or video.
  The refusal is a 413 whose message names the cap and the actual figure — a
  caller told only "too large" cannot tell which limit it hit or by how much.
- **Audio and video are exempt from the per-item cap, by design.** The only
  two workloads `MediaMaxBodySize` was raised for are a 30-60 minute recording
  that can exceed 100MB and a 15-50MB video clip (`middleware/max_body.go`). A
  single 25MB ceiling refused both — the input those lanes document as
  supported — while adding nothing, because `MediaMaxBodySize` and the item
  count already bound them. There is no total-size cap for the same reason: at
  64MB it refused the 100MB recording, and raised to the transport ceiling it
  could never fire. A cap that never fires is a number that agrees with
  nothing, so it is gone rather than left in place.
- **The measurement counts what the lanes accept, not what a filename
  suggests.** A multipart part is media when it carries a filename *or* when
  its form field is one the media lanes read binary input from (`file`,
  `image`, `audio`, `video`, `input`, `source`). `antigravity_stt` selects its
  audio by `FormName() == "file"` and reads `FileName()` only for a label, so
  counting by filename alone let that audio through every cap. In a JSON body,
  a media item is a data URI anywhere in the tree, or raw base64 in the
  `image`, `images`, `mask` and `reference` fields, which the image lanes wrap
  into a data URL themselves.
- **The media type is parsed, not substring-matched.** `measureMediaInput`
  now calls `mime.ParseMediaType`, which lowercases the type per RFC 9110, as
  every lane already did. Matching `strings.Contains(ct, "multipart/form-data")`
  sent a `Multipart/form-data` request down the JSON branch, where a multipart
  body measured as zero media and no cap fired. The same substring test was
  used to pick the model out of the body and to decide whether to rewrite it;
  both now use the same parsed value.
- **The base64 sniff accepts the alphabets the lanes actually forward.**
  URL-safe base64 (`-` and `_`) and line-wrapped base64 are both legal, and
  `codexImageDataURL` wraps a bare payload into a data: URL without
  re-encoding it, so both reach the provider at full size. Neither was
  recognised, so each measured as zero media. Wrapping inside a JSON string
  has to be escaped (`\n`, `\r`, `\t`); those escapes are accepted and skipped,
  and any other escape still marks the value as text.
- **Bytes are measured decoded, in both branches.** The JSON branch counted
  the encoded data URI and the multipart branch counted the decoded part, so
  the same image was refused as JSON and accepted as multipart. Both branches
  now report the size the payload decodes to.
- **The JSON walk streams instead of building a tree.** Unmarshaling the body
  into a generic tree allocated a second full copy of it — an 8MB body
  measured +8MB of live heap, a 32MB body +32MB — at the moment the cap was
  checked. `jsontext` tokenizes the body in place: handed a `*bytes.Buffer` it
  borrows the caller's slice, and a string value is measured from its token
  span rather than decoded. A test pins that measuring an 8MB body grows the
  live heap by less than half of it.
- **An unmeasurable body is refused, not forwarded.** A parse error used to
  read as "could not measure, so do not refuse", but the media lanes break out
  of their own parse loop on the same error and forward the file they already
  read: a 30MB audio part followed by one malformed header reached upstream
  with `items=0`, `bytes=0`, status 200. The guard now fails closed with a 400
  that names the malformed body — unless the partial measurement already
  exceeded a cap, in which case the 413 that names it is the actionable answer.
  Both STT lanes that swallowed the error (`antigravity_stt`, `elevenlabs_stt`)
  now return it instead of forwarding a truncated parse.
- **The measurement is reported back to the caller.** `X-Media-Items` and
  `X-Media-Bytes`, the same shape `X-Token-Quota-*` already uses.
- **Those two headers are gateway-owned.** Upstream response headers now go
  through `copyUpstreamHeaders`, which drops them if the provider emits them:
  a provider that emits `X-Media-Items` could otherwise replace the number
  the gateway measured with its own — the usage the gateway claims to own was
  writable by the party it measures. Unrelated upstream headers are still
  relayed.
- **The guard sits at the one funnel every media lane passes through**
  (`forwardMediaRequest`), behind the per-key access gate and ahead of model
  resolution and connection selection. Access control moved ahead of the caps:
  a key that may not call the model was answered 413 with the limit and the
  measured figure, when the answer it is owed is 403 — the limit is policy
  detail a denied key has no business learning. The two lanes that leave the
  funnel on their own path, `/v1/systemone` and the Xiaomi MiMo speech lane,
  are guarded at their own entry points.
- **The size cap is checked before the count cap.** A request carrying both
  problems used to be told its item count, which says "send fewer" rather than
  which file to shrink.

Verified with `internal/handlers/media/media_input_cap_test.go`,
`media_cap_verify_gaps_test.go` and `media_cap_review_test.go`: the item-count
cap, the per-item cap, the filename-less media part, the bare base64 image
field, the URL-safe and line-wrapped forms, the prose control, the data:-prefixed
prompt, the capitalized media type, the malformed multipart body (refused with
400, and still answered 413 when a cap was already exceeded), the audio/video
exemption, the JSON-vs-multipart byte agreement, the allocation bound, the
access-before-cap ordering, the upstream header overwrite, and the two lanes
that bypass the funnel. The shipped cap values are pinned in one test, because
every other test lowers them. The refusal tests count the upstream requests
that actually arrived and require zero — the cap has to fire before the call,
not after it.
