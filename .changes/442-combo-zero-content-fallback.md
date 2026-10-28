### fix(chat): a 200 with zero usable content must not cool the account down

A model can answer HTTP 200 and still carry nothing a client can use: an empty
assistant message, a finish of `in_progress` or `incomplete`, an empty
`choices` array. The gateway read that as a served turn, so the combo stopped
there, and the empty answer became a generic upstream failure — which cooled a
**healthy account** down, marked the provider unhealthy, and burned the same
model's remaining connections before the combo moved on.

`internal/proxy.IsZeroContentResponse` ports the upstream contract
(`decolua/9router` `8f4e380`, PR #4750) next to the check that already owns "a
200 that carries no completion". When a combo has armed the probe, such a
response is now classified as zero content instead of failing as an upstream
error: the combo advances to the next member directly, the account keeps its
rotation, the attempt is not counted as an upstream error, and it is not billed
as a served turn.

The last member still answers with its own body, routed through the normal
response path so the reply is translated into the client's wire shape
(Claude/Responses) and recorded as a normal served turn in usageHistory with
requestDetail status="success". A request that is not a combo is unchanged.
The detector uses `jsontext.Value` to tolerate non-string `reasoning_content`
and `finish_reason` without failing choice decode, prefers `message` over
`delta`, and treats empty `function_call` objects as truthy tool calls matching
upstream. The streaming path is untouched: once bytes are on the wire the
response is committed and no fallback is possible there, which is why upstream
applies the check to JSON responses only.

Verified with a table-driven unit test over both wire shapes and an
integration test that boots the real router with a two-member combo whose first
member answers 200 with an empty completion: the client receives the second
member's answer, the first account keeps its rotation, and the discarded
attempt writes no usage row. Against the pre-fix code that test fails with the
account under cooldown and the model lock set.
