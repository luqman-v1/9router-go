### fix(media): rewind POST body on direct retry and escape Edge TTS SSML

- **Safe direct fallback & SSML XML escaping** (`internal/handlers/media/tts_synthesizers.go`): updated `doDirectOrClient` to rewind `req.Body` via `req.GetBody()` before dialing `directHTTPClient`, skip direct retry on canceled contexts or `client == directHTTPClient`, and short-circuit `client == nil`; extracted `buildEdgeTTSSSML` to XML-escape `lang`, `gender`, `voice`, and `text` in `SynthesizeEdgeTTS`; and capped upstream `io.ReadAll` calls via `io.LimitReader`.
- **Tests** (`internal/handlers/media/tts_synthesizers_test.go`): added `TestDoDirectOrClient_RewindsPostBodyAndHonorsCancel` and `TestBuildEdgeTTSSSML_EscapesXMLCharacters`.
