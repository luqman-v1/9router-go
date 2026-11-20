### Added

- The contributors page is regenerated from the GitHub API on every Pages deploy. `go run ./tools/sitegen -gen-team` fetches the repository's contributors and display names and rewrites the grid in `wiki/team.md` in a deterministic order, excluding bot accounts and keeping the owner's curated byline. The Pages workflow runs it before the site builds, so the deployed roster is the current one, and commits the regenerated page back when it changed — which keeps the checkout's copy, the one a local build and the shape tests read, from falling behind.

### Fixed

- The page had gone stale in three ways: the owner card said 585 commits when the API reports 692, six contributors who landed after the page was written were missing (`ghozali25`, `blestafist`, `Xractz`, `DaDecky`, `shiwildy`, `ariefra`), and the shape test pinned exactly 24 cards — the snapshot's count — so the test kept passing on a roster that had fallen behind. The test now pins what cannot move: at least one card, exactly one owner, one profile link and one lazy sized avatar per card.
