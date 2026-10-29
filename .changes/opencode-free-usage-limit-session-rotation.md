### fix(opencode): rotate the stable session when the free tier reports its budget spent

The free tier accounts its quota per upstream session, and the official CLI
recovers a spent session the way it starts every conversation: with a freshly
minted session id and a fresh budget. The router pinned one stable session per
connection and recycled it only after 30 minutes of idle, so an agent session
in active use stayed on a spent session and answered 429 FreeUsageLimitError
forever, while the same user's OpenCode CLI on the same egress kept working.

Every free lane (opencode, opencode-zen chat/responses/messages, opencode-go)
now answers the 429 FreeUsageLimitError shape by retiring the stable session
that just failed and replaying once on a freshly minted one, alongside the
existing single direct-dial replay for a 403 FreeTierError refusal. A
client-supplied session is never swapped: it identifies the caller's own
conversation upstream. The expiry is conditional on the failed id, so a
rotation that loses a race against an already-rotated request keeps the newer
session's fresh budget. If the fresh session also answers 429, the limit is
not the session's and the error surfaces to the caller's fallback machinery
unchanged.

Together with the failover handling, the gate's two refusal shapes now behave
as egress/session verdicts rather than account faults: the account is never
cooled down, the client receives the classified 429 with a Retry-After, and a
spent session self-heals like a new CLI conversation.
