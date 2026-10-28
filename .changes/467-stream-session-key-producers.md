- fix(chat): build the stream and gemini-stream session keys with
  `translator.NewSessionKey` instead of the wall clock alone, so a coarse
  clock can no longer hand two live requests the same `*StreamState`
  (`internal/handlers/chat/forward.go:155`,
  `internal/handlers/chat/gemini_handler.go:493`).
- test(translator): give the session-key regression guard teeth on every
  platform — `sessionNow` is injectable, so a frozen clock makes the counter
  the only thing keeping keys distinct; plus a source walk that fails when a
  session key is formatted from `time.Now()` alone.
