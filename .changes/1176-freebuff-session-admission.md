### fix(freebuff): admit on /session/admission, and stop at the third-party refusal
- A freebuff session used to be admitted implicitly by its first chat call, so a seat whose admission was refused still consumed a turn and answered with a third-party refusal the client could not tell apart from a provider error.
- The lease now asks `/session/admission` first and reports the refusal as its own upstream status, so the combo falls through to the next seat instead of surfacing a refusal from a provider the request never reached.
- The switch path stubs the admission and device-key calls in its tests, which is what the closed #1176 was failing on: the tests now assert the admission round trip instead of relying on the real endpoint.
