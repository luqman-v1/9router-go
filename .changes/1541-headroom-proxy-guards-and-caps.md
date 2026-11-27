### Fixed

- **Headroom handler nil guards, timeout, and HTML cap**: guarded nil `Repo` in `headroomURL` and `headroomFlags`, added a 30s client timeout and nil-client fallback, rejected hostless Headroom proxy URLs before dialing, and bounded `/dashboard` HTML reads to 8 MiB.
