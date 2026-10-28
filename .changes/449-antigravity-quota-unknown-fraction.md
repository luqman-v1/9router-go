### fix(antigravity): keep an unknown weekly quota unknown instead of exhausted

`ParseWeeklyQuotaSummary` decoded `remainingFraction` into a `float64`, so a
bucket that reported `"remainingFraction": null` — or omitted the field — landed
on the zero value and was read as fully exhausted. The dashboard then showed a
weekly limit at 0% for a quota the upstream never described, and the 5h-session
isolation that keeps an exhausted session from dragging the weekly figure down
had nothing correct to preserve.

Both nested bucket structs now decode into `*float64`. The parse loop treats a
nil pointer as unknown and skips the bucket, so it is absent from the result map
rather than present with a fabricated zero. `disabled` buckets keep their
existing semantics: a disabled weekly bucket is skipped, a disabled session
bucket stays pinned at 0.

Verified with `TestParseWeeklyQuotaSummary_PointerDecode`, whose
`NullRemainingFraction_Omitted` and `MissingRemainingFractionKey_Omitted`
subtests fail on the pre-fix code and pass afterwards. The fixture also pins
window independence: an exhausted 5h session leaves the 85% weekly figure alone.
