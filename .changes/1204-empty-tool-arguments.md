### Fixed

- A tool call that arrives with no arguments (absent, `{}` or `null`) is now treated as a stalled loop on its first repeat instead of its third, when an earlier call to the same tool carried arguments. Clients that validate required fields — Claude Code among them — reject such a call and retry it unchanged, so the old threshold only fired after three rejected rounds. The intervention names the tool and asks for the arguments it needs.
- Tools that are always called without arguments are unaffected: the check needs an earlier call to the same tool that did supply arguments.
