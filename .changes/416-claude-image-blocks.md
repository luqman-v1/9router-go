### fix(executor): convert OpenAI image_url parts to Claude image blocks

An image sent by an OpenAI-shaped client used to reach Claude as a raw `image_url`
part. `convertOpenAIMessagesToClaude` forwarded each message's `content` as the
client wrote it, and Anthropic only defines an `image` block, so it answered
`400 messages.<n>.content.<i>: Input tag 'image_url' found using 'type' does not
match any of the expected tags`. Because that turn stays in the client's history,
every later request of the conversation failed too.

Normal use reaches it, not a hand-crafted payload: the pi coding agent returns an
image from a tool as a text-only `tool` message followed by a `user` message with
an `image_url` part, and any OpenAI SDK client sending a vision message to a
`cc/*` model does the same.

`image_url` parts are now converted in `user` and `assistant` content, in the
`assistant` + `tool_calls` branch, and inside `tool_result` content: a base64 data
URI becomes `{type:"image",source:{type:"base64",media_type,data}}` and an
`http(s)` URL becomes `{type:"image",source:{type:"url",url}}`. A part Claude has
no way to represent (`file://`, a non-base64 data URI) is dropped, because
forwarding it would only reproduce the 400 on every later turn. A turn whose
only parts were dropped is removed from the request as well, because
`content: []` is its own 400 ("content must not be empty") that would poison the
conversation the same way; the neighbours it leaves adjacent merge as usual. When
that would empty the whole conversation the turn is kept, since an empty
`messages` array is also rejected, and the provider's reason reaches the caller. This matches
upstream `open-sse/translator/request/openai-to-claude.js`, the `IMAGE_URL`
branch; the Go port had no OpenAI→Claude equivalent.

**Verified.** On a build of this change over `v1.9.13` with a real Claude OAuth
connection (`cc/claude-sonnet-5-5`), a vision message, an image inside `tool`
content, and the pi `tool` + `user` image sequence all answer `200` where they
returned the `image_url` 400 before, and a real pi session reading a PNG with the
`read` tool now describes it. New tests assert the body the upstream sees.
