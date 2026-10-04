// Package bootfx holds the one integration test that boots the real fx
// application graph rather than assembling the router by hand.
//
// The rest of the integration suite calls app.ProvideRouter directly, because
// app.DatabaseModule goes through db.InitGlobalDatabase — a process-wide
// singleton that would give every test the same database, and whose fx OnStop
// hook closes that connection for good. This package is a separate test binary
// for exactly that reason: it can afford the real DatabaseModule and the real
// ServerModule that binds a port and starts the background loops.
//
// Because of the same constraint, TestMain boots the application exactly once
// and every test shares it. A per-test boot would need
// db.ResetGlobalDatabaseForTest to hand back a live handle, and that is the one
// thing a shared, already-serving server cannot do mid-run.
package bootfx
