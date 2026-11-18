### Fixed

- Provider aliases now resolve to their canonical id when reading synced catalog limits and static provider token limits. Models published under short aliases (e.g. `oc/hy4-flash-free`) use the same context window as their canonical provider instead of falling back to the generic 128k/4k guess.
