package dashboard

import (
	"encoding/json"
	"net/http"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/service"
	"9router/proxy/internal/shutdown"
	"9router/proxy/internal/updater"
)

// The seams the endpoint works through; a test swaps them so an assertion
// cannot restart the test process.
var (
	restartSelf          = updater.RestartSelf
	restartSelfSupported = updater.RestartSupported
	serviceStatus        = service.Status
	requestStop          = shutdown.RequestStop
)

// restartDelay lets the 200 reach the client before the listener closes.
var restartDelay = 200 * time.Millisecond

// restartConfirm is the body value the endpoint requires. The dashboard cookie
// travels on a cross-site form post too, so a request that cannot set a JSON
// body must not be able to stop the gateway; matching this word also keeps the
// route safely reachable in a smoke test without a real restart.
const restartConfirm = "restart"

// HandleRestartGateway handles POST /api/system/restart.
func (h *DashboardHandler) HandleRestartGateway(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil || req.Confirm != restartConfirm {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, `send {"confirm":"`+restartConfirm+`"} to restart the gateway`)
		return
	}
	restart, err := planRestart()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "cannot restart this process: "+err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"restarting": true,
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	// Both are read here, not in the goroutine: a reader of the package vars
	// after the response would race a caller that swaps them.
	delay := restartDelay
	go func() {
		time.Sleep(delay)
		restart()
	}()
}

// planRestart decides who brings the gateway back, before anything answers
// 200: a supervisor that restarts on exit owns the restart, and a process
// nobody supervises replaces itself. Spawning a replacement under a supervisor
// would start a second process that races the one the supervisor starts.
func planRestart() (func(), error) {
	if info, ok := serviceStatus(); ok && supervisorRestartsOnExit(info.Backend) {
		return requestStop, nil
	}
	if err := restartSelfSupported(); err != nil {
		return nil, err
	}
	return restartSelf, nil
}

// supervisorRestartsOnExit reports the backends whose unit keeps the service
// alive after it exits. Cron and the Windows logon entries launch the gateway
// once and never bring it back, so those fall through to the self-restart.
func supervisorRestartsOnExit(backend service.Backend) bool {
	switch backend {
	case service.BackendSystemdUser, service.BackendLaunchd, service.BackendOpenRC, service.BackendRunit:
		return true
	default:
		return false
	}
}
