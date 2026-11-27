### Fixed

- **A browser cookie row that cannot be scanned is now an error, not a healthy empty cookie jar.** `readChromiumRows` skipped rows whose `Scan` failed, so schema drift the expiry fallback did not know about reported every cookie as missing and the caller answered "session not found" for a login that exists — a silent failure with a misleading message. The scan error is now returned, wrapped, so the two cases stay distinguishable.

- Pinned by `TestReadChromiumRowsSurfacesScanErrors`: a row whose `expires_utc` holds text (the shape schema drift leaves behind) returned `nil` before the fix and a named error after it.
