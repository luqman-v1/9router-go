### Wiki docs: the footer names the release the docs describe, and pages ahead of it are marked

The wiki reported `1.9.15-exp.16` — the build being developed — while `9router-go update` advertises `1.9.14`, the last stable release. A reader could not tell whether the pages described a release they could download or a build nobody had. The two numbers are required to differ: `release.yml` fails an experimental release whose manifest version matches the build, so this is not a mismatch to reconcile but a distinction to render.

- **`-stable-version` flag**, read from `version.json`, is what the sidebar footer now renders. `make wiki-build` passes both it and `-version`, with `STABLE_VERSION` derived in the Makefile the same way `VERSION` is. The flag falls back to `-version`, so a one-off local run without the manifest still shows a version.
- **`since:` frontmatter**, the release that first documented a page, seeded as `1.9.14` across all 21 pages. A page whose `since` is newer than the release the wiki was built from is marked **new** in the sidebar — the docs for two releases sit at the same URL, and without the mark a reader takes an experimental flag as the shipped contract.
- The comparison is on the release line and is **ordered**, not an equality test: `1.9.15-exp.16` is a build of the `1.9.15` line, and a wiki built from `1.9.15` must not flag a page documented in `1.9.14`. Equality flagged every page on a wiki built one release ahead; the parts are compared numerically, because a string compare orders `1.10.0` before `1.9.0`.
- The "Which version am I on?" page now explains both numbers and the mark.

Verified in a browser: the footer reads `Version 1.9.14` on the homepage and five other pages, with no badge while every page matches that release. The badge appears on all 21 pages when the wiki is built from `1.9.13` and disappears at `1.9.14` and `1.9.15`, which is the direction the feature depends on.
