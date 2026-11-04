### Fixed

- **errorclassify: do not park accounts on improperly formed requests** (upstream #4958): Removed the `improperly formed request` text rule that triggered `cooldownLong`. A malformed client request is an error in the caller payload, not an account outage, so it must not park healthy accounts behind a 2-minute cooldown.
