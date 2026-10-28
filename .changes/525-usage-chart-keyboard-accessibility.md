### fix(usage): make the usage chart reachable from the keyboard

The usage chart drew a set of stacked areas and a tooltip that opened under
the pointer. The drawn areas carry no text a screen reader can read, and a
tooltip that only opens on hover is unreadable to anyone driving the page from
the keyboard — the numbers the chart shows existed nowhere else.

**This is this port's own change, not an upstream port.** Upstream's chart has
none of it — no focusable points, no key handling, no ARIA, no table — so
there is no upstream commit to cite. A chart nobody can read with a keyboard
is a real gap, and upstream's chart has the same gap today, so the divergence
is deliberate and is stated here rather than presented as parity.

- **One tab stop for the chart, not one per bucket.** Every overlay used to
  carry `tabindex="0"`, so an all-time window of 3650 buckets was 3650 tab
  stops and a keyboard user could not Tab past the chart. The overlays now use
  a roving tabindex: exactly one is `0` at a time, the arrows move focus among
  them, and `Home`/`End` jump to the ends.
- **The arrows move the focus, not just the tooltip.** Binding a handler per
  point meant the keydown always read the focused element's own index, so
  `ArrowRight` moved the tooltip and left focus behind — the second press
  computed the same index again and the walk stopped after one step. The
  handler now reads the roving index and moves focus with it, so repeated
  presses keep walking.
- **The table costs nothing while it is closed.** Its rows live inside a
  `<details>`, which hides them visually but still builds them: 3650 buckets
  meant 3650 `<tr>` and 10950 `<td>` in the DOM with the disclosure shut. The
  body is now built only while it is open, which is what `AGENTS.md` §4.F asks
  for on a 1-vCPU box.
- **The table renders costs through `costIn()`, like every other cost surface
  in the dashboard.** A literal `$` showed dollars in the table while the
  tooltip directly above it showed the configured display currency, on the
  same screen for the same numbers.
- **A daily bucket label is not a unique key.** "Jan 2" repeats across a year
  boundary, and Svelte throws `each_key_duplicate` on the second occurrence,
  which takes the whole usage page down rather than just the table. The rows
  are keyed by index, which is unique by construction and the same key the
  plotted points use.
- The e2e suite covers both halves against a real Chromium: one row per bucket
  in the table, and two `ArrowRight` presses from a known start asserting the
  *focused* element moved — a single press from one start cannot tell a working
  walk from a handler that is idempotent after one step.
