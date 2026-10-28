### fix(updater): refuse an update check when no channel is configured

A fork build links `DefaultUpdateURL` and `DefaultGitHubRepo` empty on purpose:
it has its own release channel or none at all. `CheckUpdate` did not treat that
as a configuration state. It fell through to GitHub's releases API with a
coalesced repo value, so a build that was never pointed at a channel could still
reach a release endpoint it does not own, and a fork that meant "no updates"
could still be told it had one.

`CheckUpdate` now returns `update channel is not configured` before the first
network hop when either the manifest URL or the repo is empty. The refusal
happens before `checkManifest` and before the GitHub fallback, so no request
leaves the process.

The fallback behaviour is deliberately unchanged for a *configured* channel
whose manifest is dead: it still falls back to the configured repo, never to
whatever upstream's default happens to be. That is the half-configured case the
new guard could otherwise hide, so it is pinned too.

Verified with `TestParityUnconfiguredChannelMakesNoRequest`, whose test transport
fails the test if any request is attempted, and
`TestParityForkFallbackStaysOnConfiguredRepository`, which serves a dead manifest
and a live fork repo. The first fails on the pre-fix code.
