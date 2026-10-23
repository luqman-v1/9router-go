### feat(clientconfig): kilo and cline adapters, and the tool registry

The last two clients, plus the registry `--tools` validates against.

**kilo** — `~/.local/share/kilo/auth.json`, one entry under `auth`. Reset also
removes `auth["9router"]`, a leftover from an earlier schema this adapter never
wrote.

**cline** — the only client with two files and the only one that takes the bare
origin. `globalState.json` gets the provider selection, URL and model;
`secrets.json` gets the key. Both are written state-first so a failure between
them leaves the secret unset rather than set for a provider that was never
chosen, and `applyPlans` reports both backup paths when the second write fails.
Reset only undoes the selection while it still says `openai` — a user who
switched Cline back to its own provider keeps that decision.

**Registry** — nine adapters, the six documented aliases, `all` expansion, and
duplicate spellings collapsed. An unknown name is an error listing what is
accepted rather than a silent skip: a connect that quietly ignored a misspelled
tool would configure nothing and report success.

Nested key creation was added to YAML for omp: writing `providers["9router"]`
into a file with no `providers` object is the normal first run, and every key is
quoted at every level so one style holds for a repeated patch.

- Evidence: `go test ./internal/clientconfig/` passes, including S1 (round trip
  per adapter), S2 (foreign keys, comments and ordering survive), S3 (a malformed
  file is left byte-identical with no backup), and the registry assertions.
  `go list -deps ./cmd/9router-go | grep clientconfig` is still empty, so the
  gateway binary is unchanged until Task 5 wires the command.
