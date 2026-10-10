### feat(dashboard): proxy pools get an actions menu and a status dropdown

The proxy-pool header carried four controls (`Test All`, `Deploy Relay`,
`Batch Import`, `Add Proxy Pool`) and the card below it opened with a strip of
four filter pills, each showing its count in brackets. Beside the sort picker
and the two bulk-cleanup buttons, that row was the widest fixed thing left on
the page, and it wrapped to three lines on a phone.

- `Test All`, `Add Proxy Pool` and `Batch Import` moved behind one **Menu**
  trigger (`web/src/lib/ui/Menu.svelte`), the same control the top bar, quota
  tracker, API keys and provider rows already use. `Deploy Relay` keeps its own
  trigger: it is a separate task with its own submenu, not one more verb.
- The four filter pills became a counted dropdown (`Filter proxy pools by
  status`), carrying the same All / Active / Passed / Failed buckets and the
  same counts, so nothing is lost from the pills.
- New shared primitive `web/src/lib/ui/CountedSelect.svelte`. It is the
  dropdown analytics' `ViewSelect` already is — measured trigger, viewport-
  clamped panel, portalled, Escape/Tab/outside-click dismissal — differing only
  in that each row carries a count instead of a glyph. Analytics keeps its own
  component: an option there is a *view*, which is a different thing to say
  out loud than "the filter under the header", and it is not worth a third copy
  of this logic.

**Bug found and fixed on the way:** every one of those menus was
self-limiting its own width. `placePanel` was handed the panel's measured
`offsetWidth` and wrote it straight back out as an inline `width`, so a panel
was pinned to whatever width it had when measured and could never grow again —
any label wider than that floor ellipsised permanently. That is what rendered
`Add Proxy P…` in the first screenshot here, and it silently affected the top
bar, quota tracker, cache analytics and usage-section menus as well. Placement
and sizing are now separate: `placePanel` decides *where* the panel goes and
emits only `left`/`top`/`bottom`, while the panel sizes itself from its own CSS
(`w-max`, plus the caller's floor and the new `maxPanelWidth()` clamp). The
viewport clamp the helper used to apply in JS is not lost — it survives as that
`max-width`, which is the same bound.

Verified by driving the real dashboard in Chromium: the menu and dropdown open,
every label renders in full at 1440px and 448px, both panels stay inside the
viewport, and picking `Failed` updates the trigger and the list. Suites green —
311 web unit tests, 37 e2e, 3842 Go tests, svelte-check ratchet at baseline,
`vite build`.
