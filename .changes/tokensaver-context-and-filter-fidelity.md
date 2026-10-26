### fix(tokensaver): stop dropping system blocks, and make the filter selection survive real command lines

**System prompt injection destroyed block-array content.** `InjectSystemPrompt`
read a Chat system turn's `content` as a string. A turn whose content is
already a block array — what Claude Code, Cursor and most SDKs send — failed
that type assertion and fell into the empty-string branch, which replaced the
whole array with the injected prompt. Every instruction the client had put
there was deleted on the way upstream. The turn now keeps its array and the
prompt arrives as one more `{"type":"text"}` block, matching upstream
`open-sse/rtk/systemInject.js:138-150`. A block array that already carries the
prompt is left untouched.

**Anchored filter patterns now compile multiline.** Upstream anchors every
autodetect regex with `/m` (`open-sse/rtk/autodetect.js:18-26`) so a leading
banner line does not defeat detection. Go compiled the same patterns without
it, so `^diff --git ` only matched at offset zero: a tool result of
"Here is the diff:" followed by a real diff fell through to `generic-output`
and lost the diff-specific rules. Patterns now compile with `(?im)`.

**Git global options no longer hide the subcommand.** Filter command patterns
are written against the bare form (`^git\s+(?:diff|show)\b`), but an agent that
runs `git -C /repo diff` produced identical output and matched nothing, so the
result landed on the generic filter. `normalizeCommand` folds the global options
git accepts before its subcommand (`-C`, `-c`, `--git-dir`, `--work-tree`,
`--no-pager`, and the `=` forms) so the wrapper does not change which filter
owns the output.

**文言文 levels answer with 文言文.** The dashboard offers `wenyan-lite`,
`wenyan` and `wenyan-ultra` beside the English levels, but the dispatcher had
no case for them and returned the English full prompt — a 1,760-character
caveman doctrine for a user who picked classical Chinese. All three levels now
carry their own doctrine, matching `open-sse/rtk/cavemanPrompts.js:70-101`, and
share one `cavemanSharedTail` constant so the format rules and the
Auto-Clarity override reach every level from a single definition.
