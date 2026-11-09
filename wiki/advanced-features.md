---
title: Advanced Features
order: 13
section: Under the Hood
since: 1.9.14
---

# Advanced Features

Three features in 9router-go came from other projects. Where that happened, the
source is listed as a link at the end of each feature — you do not need to know
where something came from to use it.

Each feature below tells you **what it does** and **how to use it, step by step**.

---

## Persona — attach a system prompt to specific models

**Since:** v1.9.15 · **Source:** [arewedaks/9router-go](https://github.com/arewedaks/9router-go)

### What it does

You write one instruction document once, choose which models it applies to, and
every request sent to those models automatically carries your instructions at the
front. Your original message is untouched.

### How to use it

1. Open the dashboard and click **Persona** in the sidebar.
2. Write your instructions in the main editor. This is your default document.
3. To save several sets of instructions, use **Presets** — give each one a name.
4. To attach a preset to specific models, open the preset and pick the models.
   A model can only carry one preset at a time.
5. To import instructions you already have, use **Import** — paste a URL, drop a
   file, or upload a `.md` file.
6. Turn the feature on. **It is off by default**, and nothing is injected until
   you enable it.

### Covering a whole family of models at once

Instead of picking models one by one, add a **keyword rule** to a preset. The
keyword `deepseek` covers `providera/deepseek`, `deepseek-v4`, and every model
whose name contains it — across all providers and every leg of a combo.

Three matching modes:

| Mode | Matches | Example |
|---|---|---|
| `word` (default) | A whole word | `deepseek` matches `deepseek-v4` but **not** `deepseekia` |
| `partial` | Any part of the name | `seek` matches `deepseek`, `seeking` |
| `regex` | A regular expression | `^deepseek-v[0-9]` |

### Blocking a model from ever receiving a prompt

Two deny lists, both using the same grammar as the keyword rules:

- **Per-preset excludes** — models this preset must never touch.
- **Global excludes** (Settings) — outranks every preset and every exact binding.

### Which prompt wins

If several rules could apply, the order is:

1. Global exclude
2. An exact binding on the model id the client sent
3. The winning preset's own excludes
4. A keyword matching the requested model, before a keyword matching a forwarded leg
5. The longest keyword (a tie goes to the preset with the lowest id)

An exact binding to an **empty** prompt stays in force — it does not fall through
to a keyword match.

### Notes

- Injection reaches every path: single chat, Claude messages, combo fallback,
  and the fusion panel. For a combo, the switch is keyed to the combo name the
  client sends, so all legs of that combo are covered.
- A Gemini binding can also send `safetySettings: BLOCK_NONE` through the
  official API field. If a request already carries safety settings, they are
  left alone.
- **This is not the same as the Global System Prompt** in
  [Settings](settings.html). That one applies to every request with no way to
  pick models. Persona is the one you use when you need it on *some* models only.

### Before you use it to bypass a model's limits

Persona can be used to push a model past its own restrictions — a jailbreak.
Some people do this to get more out of a model than it is meant to give. Read
this first.

**You can get more out of a model.** A prompt that reframes the task often gets
a fuller answer than the model gives by default. That is the appeal, and it is
real.

**You can also lose the account.** The provider on the other end sees the same
traffic you do. A request that looks like an attempt to override its safety
rules is grounds for it to flag, throttle, or close your account — and the
decision is theirs, not yours, with no appeal and no warning you can act on.
The same applies to an API key, a subscription, and any other credential you
used to reach that model.

What that means in practice:

- **You carry the risk, not the model.** A banned account is not a bug you can
  report. The credential stops working and you start again somewhere else.
- **One account can take others with it.** If the credential is shared — a
  combo, a team key, a resold plan — the ban lands on everyone using it.
- **`BLOCK_NONE` is the loudest signal.** Turning off a provider's safety
  settings outright is the clearest possible marker that the request is not
  ordinary use. Use the prompt-based approach if you use anything at all.
- **Nothing here is a promise.** This gateway can send a prompt. It cannot make
  a provider accept one, and it cannot restore an account it decides to close.

If you want more from a model and the account matters, the safer path is to ask
the model for more within its rules — a fuller explanation, a different angle, a
longer answer — rather than instructing it to ignore them.

---

## Security — vault, guardrails, and who may spend what

**Since:** v1.9.11 · **Source:** [mydisha/keirouter](https://github.com/mydisha/keirouter)

### What it does

Three controls that keep your credentials, your prompts, and your budget safe.
All three live on the **Security** page in the sidebar.

### Credential vault

Your provider credentials are stored sealed with AES-256-GCM encryption, and only
unsealed in memory when a request needs them. The database never holds a
plaintext copy.

**How to check it:** open **Security** and read the vault status card. It tells
you whether the vault is active and how many credentials it holds.

**Rotating the master key:** use **Rotate Master Key** on the same card, and type
the new key when asked. There is no confirmation dialog — the rotation happens
when you press it.

> Losing the master key loses every stored credential. Write it down somewhere
> safe before you rotate, and keep the old one until you have confirmed the new
> one works.

### Guardrails

Scans requests leaving and responses coming back for personal data and for
attempts to override your instructions.

**How to create a policy:**

1. Open **Security** and click **New Policy**.
2. Pick a **scope** — how wide the rule applies:

| Scope | Applies to |
|---|---|
| Global | Every request |
| Provider | One provider, e.g. `openai` |
| Model | One model id, e.g. `gpt-4o-mini` |
| Chain | One combo |
| API key | One client key |

3. Pick the **detectors** — what to look for:

| Detector | Finds |
|---|---|
| PII | Email addresses, IP addresses, national ID numbers, card and IBAN numbers |
| Prompt injection | Attempts to override instructions or exfiltrate data |
| Custom pattern | Your own regular expressions |

4. Pick the **action** — what happens on a match:

| Action | Result |
|---|---|
| Allow | Detect, but never act |
| Log only | Record the decision (default) |
| Warn | Record and warn |
| Mask | Redact the match before the request is sent |
| Block | Reject the request with `400` |

5. Save. The policy is live immediately.

**The kill switch.** The **Enforce guardrails** toggle at the top of the page
stops both the request and response scans without deleting your policies — so a
false positive that blocks real traffic can be switched off in one click, and
your configuration and audit trail survive. It defaults to **on**: a policy you
wrote is a decision you made, and it must not be silently ignored just because a
setting was never written.

**Reading the audit log.** Below the policy list, the log shows every decision
with its direction (request or response), severity, and what matched. It is
polled rather than streamed, so give it a moment after a request.

### Per-key limits and budgets

Rate limits, spending caps, and which models each key may use. These are
configured per key on the **API Keys** page, or in bulk through a plan.

**Full guide:** [Governance](governance.html)

---

## Plans — one template, many keys

**Since:** v1.9.11 · **Source:** [mydisha/keirouter](https://github.com/mydisha/keirouter)

### What it does

A plan is a saved set of limits you can attach to any number of API keys. Set the
limits once, apply the plan to ten keys, and all ten follow the same rules.
Change the plan later and every key on it follows.

Use it when you have keys for different purposes — one for yourself, one for a
teammate, one for a public demo — and want each to have its own ceiling without
configuring them one at a time.

### How to create a plan

1. Click **Plans** in the sidebar. If you do not see it, turn it on in
   [Settings](settings.html) → Sidebar.
2. Click **New Plan**.
3. Fill in the limits you want. **`0` means unlimited** for every number field —
   that is the same convention the API keys use.

| Field | What it limits |
|---|---|
| Name | What you call this plan |
| Description | Optional note to yourself |
| RPM | Requests per minute |
| TPM | Tokens per minute |
| Concurrency | Requests in flight at once |
| Budget (USD) | Spending cap for the period |
| Tokens | Token cap for the period |
| Period | When the budget resets — see below |
| Alert % | Percentage that raises a warning before the cap is hit |
| Hard cutoff | On = reject with `402` once the cap is reached. Off = warn only |
| Allowed models | Which models keys on this plan may use. Leave empty for all |

4. Save.

### The four periods

| Period | Resets |
|---|---|
| Daily | Every day at 00:00 UTC |
| Weekly | Every Monday at 00:00 UTC |
| Monthly | On the 1st at 00:00 UTC |
| Total | Never — a lifetime budget |

All reset times are UTC, regardless of your timezone.

### Attaching a plan to a key

1. Open **API Keys** in the sidebar.
2. Click the key you want, or create a new one.
3. Choose the plan from the **Plan** dropdown.
4. Save.

The key list shows how many keys use each plan, so you can see the blast radius
before changing one.

### How a key's limits are decided

Three layers, applied in order:

1. Start from zero — no limits.
2. Apply the key's plan, if it has one.
3. Apply the key's own settings, where they are set to something other than zero.

So a key with no plan and no overrides behaves exactly as it did before plans
existed. A key's own settings **override** its plan — set a value on the key and
that value wins.

**Model access is the exception.** A key's allowed-models list and its plan's list
are **combined**, not overridden — a key may add models, never remove them. That
is deliberate: a plan sets the floor, and a key can only widen it.

### Deleting a plan

A plan still attached to a key cannot be deleted — the gateway refuses with
`409` rather than leaving those keys with no limits at all, which would silently
remove their ceiling. Detach the keys first, then delete the plan.

### One correction

The port plan for this feature says it is incomplete — missing periods, alert
thresholds, and the spending cutoff. That is out of date. All three exist, and a
key past its budget with hard cutoff on is rejected with `402 budget_exhausted`.

**Full guide:** [Governance](governance.html)
