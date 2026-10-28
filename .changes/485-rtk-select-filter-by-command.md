### fix(rtk): choose a filter by the command that produced the output, not by the content alone

RTK rewrote a coding agent's `bash` results by matching the filter on the
**content** of the result. A 5,638-byte `sed -n 1,45p` result came back to the
model as the 24-byte string `git: already on branch`, because the file it read
happened to mention git. The model had no way to tell the content had been
replaced, and the agent concluded its shell was broken.

The filter set already carries the answer. 53 of the 55 filters declare the
commands their rules were written for — `git-branch` declares
`^git\s+(?:branch|checkout|switch)\b` — and that declaration was never
consulted: `MatchFilter` was called with an empty command, so only the content
patterns could decide, and content matches text that merely mentions what the
filter keys on.

The command is in the request body. The tool call that produced the result
carries it in its arguments, and the gateway already reads that same tool call
to learn the tool's name.

What changed:

- the tool call's command is read from its arguments, in all three shapes the
  protocols use (Chat Completions and Responses carry the arguments as a JSON
  string, Claude's `tool_use` carries an object), under the same key
  spellings the translator's own bash-argument sanitiser accepts;
- a filter that declares commands is selected by its command patterns, or not
  at all — it is never selected by content alone while the command is known;
- a filter that declares no command (`error-stacktrace`, `generic-output`) is
  content-scoped by construction, and still matches on content.

Measured on the same request body on both trees:

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| before | 223925 | 42971 | 76 |
| after | 98637 | 43699 | 85 |

The path is **faster** after the change, not slower: the work saved by not
running a command's filter over the wrong output outweighs the cost of reading
the command. The nine extra allocations are the argument decode, and it is
guarded by a substring check, so a tool call whose arguments carry no command
is never decoded at all.

The compression is not weakened. A `git branch` result is still filtered by
`git-branch`, because the command that produced it is `git branch`.

This fixes the selection. The bounds the issue also raises — the 100-line
default and the fact that a truncation leaves no model-visible marker — are
settings and a separate change, and are not touched here.
