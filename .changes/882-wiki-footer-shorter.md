### Wiki: the sidebar footer is 15px shorter

The footer measured 85px, and 44 of those pixels were one row of icon links at a target size the rest of the sidebar does not get — the nav rows above it are 33px tall. The footer is chrome that sits at the bottom of a fixed-height column, so every pixel it costs is a pixel the nav loses.

- **The icon row is 32px, not 44.** 44 was chosen to match the nav rows and then never measured against them; it does not match. 32px is still above the 24px minimum target floor.
- **The glyph is 18px, not 20.** At a 32px target a 20px glyph leaves 6px and the hover tint barely shows; 18px leaves 7px per side so the tint reads as a plate under the icon.
- **Padding is even and smaller** — 8px top, 6px bottom, against 12px and 4px — and the release line's top margin drops to 3px.

85px → **70px**, at both 1280px and 390px. The row is still clickable at the new size: `elementFromPoint` over the centre of the first icon hits the link at both widths.
