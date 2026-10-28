### fix(chat): strip unservable legacy media from history before forwarding

An image sent turns ago rides in the history of every later request, but the
required-capabilities scan deliberately reads only the trailing user run — old
media must not pin a text conversation to a vision model. The scan's design
assumes that old media is stripped downstream; that strip existed only for a
few built-in executors, so a text-only openai-compatible upstream received the
part verbatim and rejected the whole turn with 400
`content[1].type has unsupported value "image_url"` — with no fallback, because
the request never registered as a vision request.

When the history carries media that no candidate model can serve, every media
block before the trailing user run is now replaced with a text placeholder
before forwarding. The current turn is never touched: media there is the
client's actual question, and the capacity-adapter pool is what answers it. A
combo with a vision-capable member keeps its history verbatim. As a safety net
for upstreams whose capability rows overreport vision, vision-unsupported 400
messages now classify as model-fit errors, so a combo advances to its next
member instead of surfacing the 400 as terminal.

Verified with unit tests per wire shape (OpenAI, Claude, Ollama, Gemini parts),
and e2e through the production router against a fake text-only upstream that
mirrors the reported rejection — run against the pre-fix commit, the old-turn
cases fail with the exact 400 from the report; with the fix all four e2e cases
pass and the upstream sees exactly one request.
