### test: make the suite hermetic, and pin the contracts that could not fail

Two classes of test defect, both of which made a green suite mean less than it
looked.

**The suite was not hermetic.** `TestHandleChangelog` passed for months only
because the machine running it had egress: the handler falls back to fetching
`CHANGELOG.md` over the network, and from `internal/handlers/chat` the
repository root is three levels up, not two, so the local-read path it claimed
to exercise was never taken. `internal/testnet.Seal()` replaces the default
transport's dialer with one that refuses anything but loopback and unsets the
four proxy variables, and every handler `TestMain` calls it. A test that quietly
reaches the internet now fails instead of passing on the wrong machine.

**Five assertions could not fail for the reason they claimed.** A mutation pass
over 68 headline contracts found two fully vacuous assertions — a cancellation
test that called the handler synchronously, so the timeout was only evaluated
after it returned, and a web-fetch test that posted an unconfigured model, so
the 400 came from model resolution whether or not the context was live. Both are
rewritten to run concurrently and to seed a real connection. Three weaker ones
are tightened: the five-subtest direct-route matrix asserted on values the file
built itself and is replaced by assertions through production symbols, with the
four contracts that have no production seam turned into named skips; and two
quota/watchdog branches that no test had ever taken are pinned in both
directions, so disabling the guard wholesale fails them too.

New regression tests: the G00 parity stream relay, tool-name truncation
uniqueness and map restore, the glm-5.3-flash-free outbound wire shapes, and a
strict-proxy assertion that a dead proxy dials no upstream directly.
