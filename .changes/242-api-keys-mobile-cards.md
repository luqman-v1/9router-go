### fix(dashboard): render the API-key list as cards on a phone

The follow-up to #224: after the per-row controls collapsed into one menu, the
API-key table was still unusable at phone width. A row is five columns wide by
construction, so at 390px the `overflow-x-auto` wrapper held 500px of content
inside a 292px box — the policy and actions columns sat outside the viewport and
the secret cell broke one character per line. Nothing scrolled, so nothing
looked broken; the content was simply unreachable.

Below `md` the list now renders one card per key, carrying every column the
table hid. The row menu stretches to the card's full width and keeps its
"Menu" label at phone widths, where there is room for it and a bare glyph
would be an ambiguous tap target with five verbs behind it.

The two layouts share their cells, the status pill and the row menu as
snippets, so a card cannot drift from the table row above `md`.