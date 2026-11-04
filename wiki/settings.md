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
| Logo URL | An HTTPS URL, or a local absolute path such as `/logo.png` |
| Upload image | PNG, SVG, WebP or JPEG, max 2 MB. The file is stored in the gateway's own data directory and the URL field is filled in for you |

Uploading means an operator with no public web host still gets a branded
sidebar — the image is served by the gateway itself.

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
