### fix(caps): glm-5.2 effort, glm-5.3-flash-free openai thinking, grok-4.7 500k window

Three capability rows shadowed or missed their upstream state:

- `glm-5.2`: the exact entry shadows the `*glm-5.2*` pattern, and only
  the pattern carried `ThinkingEffortSupported`, so the zai lane dropped
  `reasoning_effort` for GLM-5.2. The exact entry now repeats the flag
  (upstream b6a9ca43), and a `none` request clamps to `low` the way
  glm-5.3's does.
- `glm-5.3-flash-free`: OpenCode's own inference service rejects the zai
  `thinking` object with 400 unknown field while `reasoning_effort` is
  accepted (probed live upstream). The id now has its own exact entry
  with the openai thinking format, so thinking intents travel as
  `reasoning_effort` and the picker offers the openai effort set without
  none/thinking (upstream 32c5f30a). The zai-served `glm-5.3-flash`
  stays zai.
- `grok-4.7`: no pattern row, so it fell to the `*grok-4*` catch-all and
  the 128000/4096 limit floor; combos report the smallest member window.
  Adds the row (vision/reasoning/search/tools, openai format, 500k
  window and output), the token-limit case, and grok-4.7 in the xai and
  grok-cli catalogs plus the dashboard xai block (upstream b0655314,
  which probed the 500k limit on the Grok CLI proxy).
