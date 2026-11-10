### README: centered logo, standalone title & subtitle

The README opened with the logo and the tagline folded into one long `#`
heading and showed no mark at all. It now leads with the 9r GO mark centred
above a `9router-go` title and a separate `FREE AI Router & Token Saver with
Single Binary` subtitle, so the top of the README reads like the product.

- `README.md`: centred logo, then the title and subtitle on their own lines.
- `docs/logo.svg`: display-size (200px) copy of the 9r GO mark, referenced by
  the README. Markdown image syntax (not raw `<img>`) so `tools/sitegen` copies
  it when it renders the README into the project page.

Verified by rendering the README header in a browser (centred mark, title and
subtitle all show) and confirming `tools/sitegen` copies `docs/logo.svg` into
the generated site output.
