### fix(opencode): re-capture the free-tier harness from CLI v2.3.2

The free-lane prompt was a v2.1.4 capture, and it had drifted from what the
shipped CLI sends. Measured against the installed `opencode.exe` (v2.3.2,
`sha256 5d52d86e…b2a3a3`): the harness preamble is 9,157 characters and
declares 13 built-in tools, not the 8,247 characters and 12 tools captured on
v2.1.4.

The capture is now the CLI's own request, recorded against a local mock endpoint
rather than transcribed, and committed as a fixture
(`internal/translator/testdata/opencode-v232/capture.json`). What changed:

- **`read`, `shell`, `subagent`, `execute`** definitions drifted; the gate
  validates tool declarations byte-exact. `read` no longer says "Images and PDFs
  are presented directly to the model", and `shell` gained the timeout/background
  and `OPENCODE_SESSION_ID` sentences.
- **`experimental_image_generate`** is a new built-in and is now declared.
- **The date and the model block are rendered per request.** The old prompt
  carried `Today's date: Sun Oct 25 2026` and `# Your Model: GLM-5.3-Flash Free`
  for every free-tier id, on every lane, forever. A real CLI sends its own local
  date (the capture says `Sun Nov 29 2026` while UTC was still 2026-11-28), so
  the renderer uses the gateway host's local date and the requested model.
- **The harness bullets are no longer stripped.** `stripOpencodeCodeMode` deleted
  four bullets that are part of every genuine CLI prompt while leaving a fifth
  shell bullet behind, so the prefix diverged from a real request at the third
  bullet. Only the Code Mode section and the trailing worktree sentence are
  removed now; the prefix stays byte-exact up to `# Code Mode`.
- The `# Your Model (this session)` correction note is gone with the frozen block
  it corrected.

Verification: `TestOpenCodeHarnessMatchesCLICapture` renders the template with the
capture's own values and asserts byte equality with the recorded system prompt;
`TestOpenCodeGenuineToolsMatchCLICapture` compares all 13 definitions and
parameters, allowing only the documented Linux-persona swap in `shell`.
`TestOpenCodeHarnessRendersCurrentDateAndModel` and
`TestStripOpencodeCodeModeKeepsHarnessBullets` pin the two regressions.

Live, through the real gateway with `omp` as the client: two-turn tool calling on
the shaped lane (`read` emitted by the model, executed by the client, result sent
back and answered) and the dashboard's connection test both completed. The
upstream gate still answers `429 FreeUsageLimitError` for every free model on this
egress with a `Retry-After` that counts down to midnight UTC — that is the daily
free budget, not the prompt, and it is why the prompt change cannot be confirmed
against the gate until the window resets.
