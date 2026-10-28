### fix(opencode): honor low|high|max reasoning efforts for zen free deepseek-v4.1-flash

`oc/deepseek-v4.1-flash-free` fell through to the generic `*deepseek-v4.*`
level set, so the picker advertised medium/xhigh that the free id does
not serve, and the DeepSeek collapse turned every `low` request into
`high`.

The id now has its own level row (`low|high|max`, placed above the
generic glob) and the DeepSeek clamp preserves `low` whenever the
declared level set includes it: zen's upstream takes the level verbatim,
unlike DeepSeek's own API. Models whose set omits `low` keep the
collapse (upstream decolua/9router 313559f4).

Note: mimo-v2.5-pro declares `low`, so its `low` requests also go out
verbatim now — the same behavior change upstream's fix made for every
deepseek-format model whose declared set carries `low`.

Verified: `TestGetThinkingLevels_Parity` rows for both oc lanes and the
new `TestClampDeepseekEffort` cases.
