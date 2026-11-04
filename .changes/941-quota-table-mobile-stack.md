### fix(dashboard): stack quota rows vertically on narrow mobile viewports (#941)

- On mobile viewports below 390px, the horizontal QuotaTable row let the w-36 progress bar, usage text, and reset badges collide and overflow past the card edge.
- Switched each row to `flex-col sm:flex-row` so the quota bar, value text, and reset badges stack vertically on small screens and return to a horizontal row from the `sm` breakpoint up, using existing Tailwind tokens only.
