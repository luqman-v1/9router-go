### GitHub Copilot embeddings: wrap bare string input into an array

When forwarding an embeddings request to GitHub Copilot (provider resolved as
`github`/`gh` or `copilot`), Copilot requires `input` to be an array of strings.
A bare string (`"input": "hello"`) was forwarded verbatim and rejected upstream.
Now a bare string is wrapped into `["hello"]` before forwarding; an existing
array is left untouched, and bodies without `input` are unchanged.

Verified: `go test ./internal/handlers/media/` passes; new unit tests cover
wrapping a bare string, leaving an array intact, and non-Copilot providers
passing a bare string through unchanged.
