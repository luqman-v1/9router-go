---
title: Settings
order: 14
section: Using the Gateway
since: 1.9.14
---

# Settings

Everything on this page lives in **Settings** in the dashboard. This page walks
through each card in the order it appears, says what it is for, and tells you
what to type or pick.

> **One Save button.** The button at the top right, **Save Changes**, writes
> everything on this page. Most cards have no Save of their own. If a value looks
> like it did not stick, press Save first.

---

## Runtime Storage

Shows where your data lives and lets you move it in and out.

| Field | What it is |
|---|---|
| Database location | Shown for reference: `~/.9router/db/data.sqlite`. Change it with `DATA_DIR` or `DB_PATH` — see [CLI](cli.html) |

Two buttons:

- **Download Backup** — saves your dashboard data as a `.json` file. You will be
  asked for your password first.
- **Import Backup** — restores from that file. Accepts `.json` or `.zip`.

> A backup is a copy of your dashboard **data**, not a byte-for-byte copy of the
> live database file. It restores your settings, connections, and keys — it is
> not a substitute for backing up the SQLite file itself. See
> [Database](database.html).

---

## Security & Password

Who can open the dashboard, and for how long.

| Field | Options | What it does |
|---|---|---|
| Require login | on / off | When off, anyone who can reach the dashboard is let in. **Turn this on for anything reachable from outside your machine.** |
| Require Password on Localhost | on / off | When on, even `localhost` asks for a password |
| Session Timeout | `15m`, `1h`, `24h`, `7d`, `never` | How long you stay signed in before being asked again |
| Change Master Password | button | Opens three fields: current, new, confirm. New password must be at least 6 characters |

The default password on a fresh install is `123456`. Change it before exposing
the dashboard to anything but your own machine.
### First-run setup wizard
A fresh install opened **from the machine itself** (a loopback peer) with no
password set yet skips the login form and shows a two-step wizard instead, so
nobody has to type `123456` to get in and then change it:

1. **Password** — choose and confirm the master password directly. Two
   show/hide toggles mirror the login form's eye button, because a long
   generated password is easier to confirm when it can be read back.
2. **Mode** — *Personal* or *Advanced*. Personal writes the sidebar hide list,
   trimming the dashboard to the pages one person uses; Advanced leaves the
   sidebar exactly as it always was. Neither choice touches the route table, so
   nothing becomes unreachable and the choice is reversible from **Settings →
   Sidebar**.

The wizard applies only on a fresh install reached over loopback. Opening the
dashboard from another host, or after a password exists, gets the ordinary login
form.

### Single Sign-On (SSO)

Replaces the password login with your organisation's identity provider.

| Mode | What to fill in |
|---|---|
| **Password Only** (default) | Nothing — this is the password login above |
| **OpenID Connect (OIDC)** | Issuer URL, Client ID, Scopes (default `openid profile email`) |
| **SAML 2.0** | EntryPoint (SSO URL), SP Entity ID, X.509 certificate in PEM format |

The login button text is set by the mode you pick, so a user sees "Sign in with
OIDC" or "Sign in with SAML SSO" rather than a password field.

---

## Default Routing Strategy

How the gateway chooses between your connections when you have not set up a
combo. See [Architecture](architecture.html) for the details.

### Connection Fallback Strategy

| Option | Behaviour |
|---|---|
| **Failover** | Try the next connection when one errors or is rate-limited |
| **Round Robin** | Spread requests across every connection in turn |
| **Sticky Round-Robin** | Keep using the same connection until a limit is reached, then rotate |
| **Least Busy** | Send to the connection with the fewest turns still in flight, so a long streaming reply keeps its key out of the next pick |

If you pick Sticky Round-Robin, one more field appears: **Sticky Request
Limit** — how many requests go to the same connection before rotating (1–100).

### Combo Routing Mode

| Option | Behaviour |
|---|---|
| **First Model** | Always start from the primary model in the combo |
| **Round Robin** | Rotate which model the combo starts from |

### Force Fallback

When **every** account of a provider is cooling down, send the request through
whichever account frees up first instead of failing outright. Upstream can still
reject it — this buys a chance, not a guarantee.

---
## Rate limits
Two ceilings answer two different questions.

**Per key** — how much may one caller spend? Set it on the key itself (API Keys
→ a key → policy) as `rateLimitRpm`, `rateLimitTpm`, `rateLimitRph`,
`rateLimitRpd`, `rateLimitConcurrency` and `queueTimeoutMs`. Plans record the
same ceilings (`rpmLimit`/`rphLimit`/`rpdLimit`) for the allowance a customer is
sold. RPM alone cannot express a daily allowance: a key answering ten requests a
minute can still spend a whole day's quota before anyone notices. All four
windows are enforced by one admission call, in FIFO order — a request waits for
a free slot up to the queue timeout, then gives up with `Retry-After`, and a
client that hangs up leaves the queue instead of being dispatched upstream for
nobody.

**Per client IP** — how much may one host spend? A per-key limit protects each
key; it does not protect the upstream account from a caller that holds, or
mints, many keys, because every key spends its own fresh budget. The host
ceiling (`perIpRpm`, `perIpRph`, `perIpRpd`, `perIpConcurrency`) is checked
*before* any key window and refuses immediately instead of queueing: it is a
flood guard, not a fairness queue. All four zero means the axis is off, which is
every install that never configured it, so a default install pays nothing for it.

The global fallbacks — `rateLimitEnabled`, `defaultRpm`, `defaultTpm`,
`defaultConcurrency`, `rateWindowSeconds` and the four `perIp*` keys — are plain
settings-row values; there is no dashboard card for them yet, so they are written
with `PUT /api/settings` (a JSON object of the keys above, which is what the
settings page itself uses). The per-IP values are handed to the limiter even when
the master rate-limit switch is off, so an operator who configured only a host
ceiling still gets it. See [Security](security.html) for which client address is
counted behind a proxy.

## Per-model request caps
A cap for endpoints **you** run — `openai-compatible-*` and
`anthropic-compatible-*` — where one hot model could otherwise drain a shared
endpoint's quota for every other model on it. Stored as
`settings.perModelRateLimit`:

```json
{
  "perModelRateLimit": {
    "enabled": true,
    "limit": 120,
    "windowMs": 60000,
    "overrides": {
      "openai-compatible-lab": { "limit": 30 },
      "openai-compatible-lab/gpt-6": { "limit": 10, "windowMs": 30000 }
    }
  }
}
```

- A **nil block is off**, and so is a block written without `enabled: true`: this
  is net-new quota semantics, so a half-written config must not start refusing
  requests. Once the block exists it applies unless a scope says
  `enabled: false`.
- Precedence is per-model override → per-provider override → global block →
  built-in default (120 requests per 60 s). Values are clamped, and a limit that
  parses to zero falls back to the default rather than to "zero requests
  allowed", so a typo cannot wedge chat.
- The bucket is per scope, provider and model. Scope is the client session id
  when the client sends a stable one, else a short non-reversible hash of the
  router API key, else one shared bucket — so one customer's hot model never
  blocks another customer's, and never blocks a different model on the same
  provider.
- A refusal is `429` with `Retry-After` in whole seconds and fails over to the
  next combo member. It is marked **local**, so failover does not cool a
  credential that was never exercised. Probes (`POST /api/models/test`, the
  auto-ping) are exempt: they report the model, not the limiter.
- In memory and bounded (5,000 buckets, oldest touched evicted). No schema
  change and no timer; windows reset on restart, which is the right behaviour
  for a rate limiter.

## Request timeout
`totalRequestTimeoutMs` bounds one chat request **end to end — headers plus
body**. It is off (`0`) by default, and the dashboard copy keeps it out of the
way until asked for: it exists to reclaim a hung upstream, not to cut a slow
one, and reasoning models legitimately stream for many minutes. A request cut by
it is reported as a timeout rather than as a client disconnect. Per-combo
`firstTokenTimeoutMs` is the narrower tool — see [Combos](combos.html).

## Restarting the gateway
**Settings → Restart gateway** stops and starts the process without leaving the
dashboard. The endpoint behind the button is `POST /api/system/restart`, which
requires a JSON body — `{"confirm":"restart"}` with
`Content-Type: application/json` — deliberately: a cross-site form post cannot
set that body, so a page on another origin cannot stop your gateway. It answers
`200` first and restarts shortly after, so the call has a reply to return.

Where a supervisor will bring the process back (systemd user unit, launchd,
OpenRC, runit — and only when it is actually running), the endpoint asks for a
graceful stop and lets the supervisor start it again. Everywhere else — cron,
the Windows logon entry, a bare `9router-go start`, Docker — it restarts itself
in place. In-flight streams do not survive either path; use the plain Shutdown
button when you want the drain first.

---

## Observability & Storage Retention

Controls what the gateway records about your traffic, and for how long. Turning
recording off entirely is the **Zero-Trace / Headless** option.

### Observability

| Field | Options | What it does |
|---|---|---|
| Enable Observability | on / off | Master switch for request recording |
| Observability Mode | `full`, `errors_only`, `off` | `full` records every request; `errors_only` keeps only failures for debugging; `off` records nothing |
| Diagnostics Retention | `1d` … `30d`, or `Unlimited` | How long diagnostic rows are kept. `1d` suits a low-memory box |
| Max Request Records | number (min 500, step 500) | Hard cap on stored diagnostic rows |
| Usage Ledger Retention | `Off`, `30d` … `365d` | How long the usage ledger is kept. **`Off` (keep forever) is the recommended default** — the ledger drives your budgets, so pruning it moves spend into per-key carryover |

### Storage numbers

Four figures, read live:

| Figure | Meaning |
|---|---|
| Database File Size | Size of the SQLite file on disk |
| Request Details | How many diagnostic rows are stored, and since when |
| Usage Ledger | How many usage rows are stored |
| Reclaimable | Space a VACUUM would give back |

If the numbers show `—`, the stats read failed — the card says so and offers a
**Retry** button rather than hiding the actions.

### Three buttons

| Button | What it does |
|---|---|
| **Prune Expired Records** | Deletes diagnostic rows past the retention setting |
| **Prune Usage History** | Deletes usage rows past the ledger retention. Shows a preview of how many rows and which date first, and asks you to type `PRUNE` to confirm |
| **Compact Database (VACUUM)** | Returns unused pages to disk. Shrinks the file; does not delete data |

> Pruning usage history is permanent, but it is not lossy for your budgets:
> the tokens are **carried over** into per-key budgets, so a key that already
> spent its quota is not given a fresh one by the cleanup. The confirmation
> tells you how many tokens will be carried.

---

## Global System Prompt & Anti-Poisoning

Instructions added to **every** request, regardless of which model or combo
serves it. This is the broadest of the three prompt-injection surfaces — the
other two are [Persona](advanced-features.html) (per model) and
[Token Saver](token-saver.html) (style only).

| Field | Where it goes |
|---|---|
| **Prefix** | The very start of the system prompt |
| **Suffix** | The very end, after everything else the model reads |

It applies to OpenAI Chat, Claude Messages, and Responses bodies alike.

> **This applies to every request, with no way to pick models.** If you only need
> it on *some* models, use [Persona](advanced-features.html) instead — that one
> binds a prompt to the models you choose.

**Which to use for what:** put identity rules in the prefix — who the assistant
is, what it must never claim. Put non-negotiable constraints in the suffix,
because the suffix is the last thing the model reads before your content, and
later instructions tend to outweigh earlier ones.

**Load Anti-Poisoning Preset** fills the prefix with a rule that forbids the
model from renaming or aliasing provider names — useful if you have seen a model
write `opencode-go` as `opencode` in code or docs.

---

## White-label branding

Rename the product for anyone you share the dashboard with.

| Field | Notes |
|---|---|
| Application name | Shown in the sidebar. Leave blank for `9router-go` |
| Application description | Up to 200 characters |
| Tagline | Up to 80 characters, shown beneath the app name on the login screen |
| Logo URL | An HTTPS URL, or a local absolute path such as `/logo.png` |
| Login logo URL | The logo tile on the login screen; falls back to `/favicon.svg` when unset |
| Tray app name | The Windows tray companion's first menu item and tray title, up to 60 characters; falls back to `9Router Go` when unset |
| Tray icon URL | The tray icon; a broken URL falls back to the embedded `tray.ico` |
| Upload image | PNG, SVG, WebP or JPEG, max 2 MB. The file is stored in the gateway's own data directory and the URL field is filled in for you |

Uploading means an operator with no public web host still gets a branded
sidebar — the image is served by the gateway itself. Every URL field takes
the same HTTPS-or-local-path rule.

---

## Display

How the dashboard looks. These are your preferences, and they apply immediately.

| Setting | Options | Effect |
|---|---|---|
| Unit abbreviation | on / off | Large numbers as `1.2M` / `3.4B` instead of `1,200,000` |
| Advanced settings | on / off | Unfolds the currency rate grid, the Sidebar card, and the Usage panel card below. Folding them back changes nothing about what they do |
| Provider card toggle | on / off | `on` keeps the on/off switch on every provider card visible; `off` reveals it on hover |
| Animation level | Full / Reduced / Minimal / Off | How much the dashboard animates. Minimal drops blur and transitions but keeps loading spinners; Off stops those too |

### Currency

Every cost is measured in USD and shown in the currency you choose here.

| Setting | Effect |
|---|---|
| Display currency | Pick from the catalog — USD, IDR, EUR and more |
| Show USD next to the converted amount | Displays both, e.g. `Rp 16.500 (USD 1.00)` |
| Rates | One row per currency: how much one USD buys. Edit freely |
| Update currency | Refreshes every rate from a public provider |

The line under the grid shows the current rate for your selected currency, so
you can see what you are about to save before saving it.

---

## Sidebar

Folded behind **Advanced settings**. Decides which entries appear in the sidebar
and in what order.

### Presets

| Preset | What it hides |
|---|---|
| **Default** | Plans and Persona |
| **Advanced** | Nothing |
| **Simple** | Media providers, CLI Tools, Skills, Console Log, Plans |
| **Super-lite** | Everything except Endpoint & Key, Providers, Combos, Usage, Settings, Security |

A preset fills the visibility switches and **leaves the arrangement alone** —
because "what is on the sidebar" and "in which order" are two different
questions.

### Arranging entries

Each row has a drag handle and two arrows. Drag a row onto another to place it
above or below; the arrows do the same thing from the keyboard.

A row dropped into a different container is refused rather than misplaced: a
media sub-entry has no place among the main entries, so the arrangement comes
back unchanged.

**Reset order** puts every entry back to the order this build declares.

> Settings can never be hidden — the panel you are reading lives behind it.

---

## Usage dashboard

Folded behind **Advanced settings**, next to the Sidebar card. Controls which
panels appear on the Usage page and in what order, per section — Overview,
Details, Cache Analytics, and Compression Analytics each have their own list.

Same controls as the Sidebar: presets, per-panel switches, drag or arrows to
reorder. One Save covers both cards.

---

## Failover Health Cache Reset

Also on the Settings page, below the main grid. When a provider's credential hit
a `429` and was placed in cooldown, this clears the lockout without waiting for
the timer.

1. Pick the provider from the dropdown.
2. Click **Reset Cooldown**.

Use this when a provider has recovered but the gateway is still holding the
account out. It does not touch the provider's own state — only what this gateway
remembers.

---

## Model Health

The last card. Shows what the automatic model-health loop found, and lets you
re-enable a model it disabled. See [Usage & Analytics](usage-analytics.html) for
how the loop decides.

That loop is scheduled. For a model that is failing *right now*, see the
[Model Breakers](advanced-features.html#model-breakers--one-bad-model-must-not-park-the-whole-provider) page — it watches live
traffic and holds one provider/model pair out of rotation without touching its
siblings.
