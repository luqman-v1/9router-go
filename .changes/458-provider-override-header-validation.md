### fix(db): validate provider-override headers against the real grammar

`validateOverrideHeader` accepted only alphanumerics and `-` in a header name.
That is narrower than a token: RFC 7230 §3.2.6 allows a dozen punctuation
characters, and vendors use them (`X-!#$%&'*+.^_\`|~`). A legitimate header was
rejected as invalid, which is a bug in the validator, not in the request.

It is also more permissive than the grammar on values. The check was
`strings.ContainsAny(value, "\r\n")`, so NUL, DEL, and the other C0 controls
passed validation and reached `net/http` — which either refuses them or lets a
malformed header out.

Name validation now walks the full tchar set. Value validation rejects CR, LF,
NUL and DEL, and every remaining C0 control except HTAB, which the grammar
allows. Both checks answer for the same reason: *would this survive being
written as an HTTP field line?*

Two case-variant spellings of one header are now a conflict rather than a
silent last-one-wins. `net/http` sends one header per canonical name, so
`X-Token: A` and `x-token: B` in the same payload would send whichever the map
iterated last. Identical values under two spellings are still accepted, since
they send the same thing.

Verified with `TestProviderOverrides_SecuritySemantics`. On the pre-fix code six
of its assertions fail — two legal names rejected, three illegal values
accepted, and the case conflict missed.
