### Added

#### Jailbreak prompt injection (per model, provider, and combo)

- Ports the prompt-injection system from `arewedaks/9router-go`: an operator
  saves an `agents.md`-style document once and binds it to specific models, and
  every request to those models carries the prompt prepended ahead of the
  original content.
- Injection is opt-in and default-off, and it is wired into every forward path —
  single chat (OpenAI), Claude single, combo fallback (OpenAI + Claude), the
  fusion panel, and the fusion judge — so it covers every model, provider, and
  combo. For a combo or fusion the toggle is keyed on the combo name the client
  sends, which arms every leg of that combo.
- Preset library with per-model binding, plus a Gemini-native lever: when a
  binding enables it, `ForwardGemini` adds `safetySettings` `BLOCK_NONE` through
  the official API field. A body that already carries `safetySettings` is left
  untouched.
- Storage reuses the existing `kv` table under scope `jailbreak` (no schema
  migration): presets are `doc:<id>`, model bindings `model:<id>`. Deleting a
  preset auto-unbinds every model armed to it.
- Dashboard API: `GET/PUT /api/jailbreak`, preset CRUD under
  `/api/jailbreak/presets`, and `GET/PUT /api/jailbreak/models`. The model id
  travels in the request body rather than the path because ids contain `/` and
  `:` (`tkh/mimo-v2.6-flash:free`), which a path parameter stored still-encoded.
- New Svelte 5 `JailbreakView` behind a `Jailbreak` sidebar tab to manage the
  default document, the preset library, and per-model bindings.
- Verified: `go test ./internal/db/... ./internal/translator/... ./internal/proxy/... ./internal/handlers/chat/... ./internal/handlers/dashboard/...` all pass, including a combo integration test proving the injected prompt reaches the upstream body for every leg and a control proving an unarmed combo forwards untouched; `bun run build` succeeds and the svelte-check ratchet holds at baseline (0 unresolved identifiers).

#### Keyword and exclude rules for the same presets

- A preset now carries keyword rules, so one preset covers a model family
  across providers and combo legs instead of one armed id at a time: keyword
  `deepseek` covers `providera/deepseek`, `providerx/deepseek-4-pro`,
  `deepseek-v4`, and every combo leg carrying one of those ids, with no
  per-model setup.
- Three modes per rule: `word` (default — a whole token or token run, so
  `deepseek` does not match `deepseekia`), `partial` (substring), and `regex`
  (RE2, unanchored, case-insensitive with a `(?-i)` opt-out). Word and partial
  rules are stored lowercased; a regex is stored verbatim because lowercasing
  would corrupt escapes and character classes.
- Two deny lists share the same grammar: per-preset excludes, and one global
  exclude list (`GET`/`PUT /api/jailbreak/excludes`) that outranks every
  preset and every exact binding.
- Precedence, highest first: global exclude, exact binding on the id the
  client sent, the winning preset's excludes, a keyword on the requested id
  before a forwarded leg id, then the longest keyword (ties to the lowest
  preset id). An exact binding to an empty preset stays authoritative and does
  not fall through to a keyword.
- The combo and fusion paths resolve against the leg ids as well as the combo
  name (`provider/model` and the bare model), so a combo whose legs come from
  different providers is covered by one keyword.
- Resolution reads one immutable in-memory snapshot (bindings, presets, rules,
  compiled patterns) invalidated on every jailbreak write, so a request does
  no extra queries and compiles no regexes — the hot path stays at zero
  queries where it was two.
- Dashboard: a shared `KeywordRules` editor for the include and exclude lists
  on the default document, every preset, and the global exclude card, with the
  same validation the API applies (a bad regex is refused with the reason
  before the request is sent) and a hint when a typed binding id is already
  covered by a keyword.
- New `docs/jailbreak.md` is the usage guide: presets, the three arming
  methods, the precedence order, a worked example, limits, the REST API, and
  how to verify an injection end to end.
- Verified: `go test ./internal/db/... ./internal/handlers/...` pass, including
  table-driven matcher tests, precedence and cache-invalidation tests, and
  end-to-end tests proving a keyword-armed preset reaches the upstream body on
  the single-model and combo-leg paths while a preset exclude blocks it;
  `bun test src scripts` passes (523 tests) and the svelte-check ratchet holds
  at baseline with 0 unresolved identifiers.

#### Import a prompt collection from GitHub

- `POST /api/jailbreak/import-github` accepts `owner/repo`,
  `owner/repo@branch`, and the `github.com`, `raw.githubusercontent.com` and
  `api.github.com` URL forms, and writes one preset per prompt file, so a
  repository such as `togg53192-cmd/jailbreaks` loads in one request instead of
  one paste per model.
- Each preset's keywords are derived from its file name as a prefix chain:
  `deepseek v4.md` yields `deepseek`, `deepseek-v4`. Resolution prefers the
  longest matching rule, so the file named after a model beats a sibling that
  only shares the family token, and a name longer than the model id
  (`kimi-k3-agents.md` for `kimi-k3`) still resolves. The chain is capped at
  the storage bound so an import never produces a preset the store refuses.
- Every file is attempted on its own: a dead link or an empty file is reported
  by path with its reason and the rest still imports. `README*`,
  dot-directories, non-`.md`/`.txt` files, files past 512 KB, and anything past
  200 files or 8 MB are skipped and named in the response.
- Only GitHub hosts are reachable, redirects re-checked against the same
  allowlist: the target is operator input, and following it anywhere would turn
  the dashboard into a proxy for the gateway's own network. Responses are
  size-bounded, and files are fetched through a bounded pool of four so a large
  collection cannot spawn unbounded work on a one-vCPU box.
- Re-running an import updates the presets it wrote rather than duplicating
  them, so refreshing a collection is safe.
- Dashboard: an "Import from GitHub" card on the Jailbreak view with the
  accepted input shapes, the imported presets with the keywords each one
  matches, and the skipped files with their reasons.
- Verified: `go test ./internal/handlers/dashboard/...` covers the target
  parser (every accepted shape plus traversal, non-GitHub host and internal-host
  rejection), the keyword chain, the candidate filter, an end-to-end import
  against a fake GitHub proving the presets resolve for `providerx/deepseek-v4`
  and `kimi-k3` while an uncovered model resolves to nothing, the refusal path,
  and the handler's 400-versus-502 mapping; a live import of
  `togg53192-cmd/jailbreaks` wrote its presets and injected a request for
  `deepseek v4` on a provider the collection never names.

#### Upload a prompt file, and three ways to fill a preset

- `POST /api/jailbreak/presets/upload` takes one file as
  `multipart/form-data` (field `file`) and writes it as a preset, so a prompt
  kept as a file lands without being pasted. It shares the derivation with the
  GitHub import — same id, name, source marker and keyword chain — because both
  paths end in one function rather than two conventions.
- The request is bounded before it is parsed, so a multipart body cannot stream
  past the ceiling into memory; a file past 512 KB is 413, an empty file is 400
  with the reason, and a missing or non-multipart body is 400. Only the base
  name of the uploaded file is used, so a client cannot name a preset after a
  directory.
- Dashboard: a file input in the import card, with the result shown the same way
  as a GitHub import. The import card now documents all three ways to fill a
  preset — write by hand, upload a file, point at a URL — and the manual path
  stays the only one that derives nothing.
- `request()` in the API client no longer forces `Content-Type:
  application/json` when the body is a `FormData`, which is what makes the
  upload reachable from the dashboard at all.
- Registered in both route tables (`internal/handlers/router.go` for
  production, `dashboard.RegisterRoutes` for tests).
- Verified: `go test ./internal/handlers/dashboard/...` covers the upload
  writing a preset with the derived chain and resolving for
  `providerx/deepseek-v4`, a client-supplied path being reduced to its base
  name, an empty file and a missing file field being 400, a JSON body being
  400, and a file past the ceiling being 413; `bun run build`, `bun test src
  scripts` (523 tests) and the svelte-check ratchet all hold. Live: all three
  paths exercised against the running gateway — a raw.githubusercontent.com
  file URL, a `curl -F` upload, and a hand-written `PUT /api/jailbreak/presets`
  — each writing a preset that injects on its model.

#### Import from anywhere, and a kind on every preset

- The import field is no longer GitHub-only. `POST /api/jailbreak/import`
  (renamed from `import-github`) accepts a GitHub repository, a single file on
  GitHub, or any other http(s) URL, which is read as one prompt file named
  after its last path segment — so `https://paste.example.com/prompts/SOUL.md`
  produces the same preset as dropping a file called `SOUL.md`. A URL with no
  usable segment names its preset after the host, and a page served as HTML is
  reduced to its readable text rather than stored as markup.
- The **Import prompts** card is a drop zone: dropping a file uploads it,
  dropping a URL fills the field and imports it, and the file input still
  works. The card names all three ways to fill a preset — write by hand, drop
  a file, point at a URL.
- A non-GitHub URL is checked against the gateway's own network at connect
  time, through the dialer's Control hook, so the address that is dialled is
  the address that was checked: loopback, the private ranges, link-local
  (cloud metadata), CGNAT, multicast and the reserved blocks are refused, and
  every redirect is re-checked. `JAILBREAK_IMPORT_ALLOW_PRIVATE=1` lifts it for
  an operator importing from their own network; it is off by default.
- Presets now carry a **kind**: the instruction-file convention a prompt came
  from. `SOUL.md`, `PERSONA.md`, `LOAD.md`, `AGENTS.md`, `CLAUDE.md` and the
  other recognised names label the preset automatically, shown as a badge in
  the preset list and in the import result, editable in the preset editor with
  a datalist of the recognised kinds. A prompt named after a model carries no
  kind. The label changes nothing about matching — keywords and bindings decide
  that — it is there to tell an instruction file from a model prompt in a long
  list.
- `request()` in the API client keeps the FormData fix, and the import method
  is renamed to match the endpoint.
- Verified: `go test ./internal/handlers/dashboard/...` covers the target
  parser for every accepted shape plus scheme and host rejection, the public-IP
  predicate across the private, loopback, link-local, CGNAT, multicast and
  unique-local ranges, the dial guard refusing loopback and honouring the
  opt-in, an end-to-end import from an arbitrary URL writing a SOUL preset, the
  same server being unreachable without the opt-in, HTML reduction, and the
  handler's 400-versus-502 mapping; `bun run build`, `bun test src scripts`
  (523 tests) and the svelte-check ratchet at baseline.

### Changed

- The dashboard surface is named **Persona** in the sidebar, page title, and nav presets. The mechanism is narrower than the old name suggested: it prepends a system prompt of the operator's own, and nothing about it judges what that prompt says. The How-it-works panel says plainly that a persona is also the usual way to apply jailbreak prompting. The nav id and the `/dashboard/jailbreak` route are deliberately unchanged — the id is the storage key for every deployment's saved sidebar arrangement, and the path is what operators already have bookmarked.
- Import is three tabs inside the New preset modal — Write, From files, From URL — instead of a card of its own, so one button is the only way in. The Paste a prompt tab was dropped rather than moved: it did exactly what Write already does, and two tabs performing one task is one tab too many.
- Test match is a modal rather than a card. It answers one question occasionally, and a full card cost as much vertical space as the 53-row preset table above it.
- The Add-binding modal's tabs are the segmented control the Proxy Pools Bulk Add dialog uses, so the two "one by hand vs many at once" dialogs match. The default tab is Single.
- Editing a binding reuses that modal's Single tab instead of opening a second one: the id field goes read-only and an on/off checkbox appears, which is the whole difference.
- Batch controls on both lists are two rows — select-all and the selected count on the first, the actions that apply to the selection on the second. One row held both, so the actions pushed the count sideways as the selection grew. The selected count is a badge.
- Binding rows split the provider prefix into a chip, so a long id truncates the part that says the least.
- `Modal` takes an optional icon, rendered beside the title, so New preset, Add model bindings, Edit model binding, and Test match all read the same.

### Fixed

- The bulk model filter did nothing. `bulkArmFiltered` was a `$derived.by` that also wrote `bulkArmRegexError`, and a derived that writes to another state is not re-tracked — the input accepted text, the count stayed at 200, and nothing filtered. The validity check and the message are their own deriveds now, and the filter reads them.
- The Add button opened the Bulk tab, not Single. It was wired as `onclick={openBindModal}`, which hands the MouseEvent in as the tab, so neither tab was selected and the panel fell through to Bulk.
- Both lists keep their filter on screen when a query matches nothing. The field used to live inside the `{:else}` branch, so a typo made the only way to clear it disappear.
- The live catalog now loads when the Bulk tab is clicked, not only when the modal is opened on it — the tab is reachable from Single without reopening the modal, so opening it was never a reliable trigger.
