package oauth

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/handlerutil"
)

const (
	callbackRelayTTL         = 5 * time.Minute
	callbackRelayMaxEntries  = 64
	callbackRelayMaxValueLen = 4096
)

type callbackRelayEntry struct {
	raw       string
	errorCode string
	errorDesc string
	at        time.Time
}

// callbackRelay bridges browser OAuth redirects (/callback on localhost) to
// cross-origin dashboard tabs (e.g. http://127.0.0.1:<port>/dashboard) where
// localStorage, BroadcastChannel, and COOP-severed window.opener cannot reach.
type callbackRelay struct {
	mu      sync.Mutex
	entries map[string]callbackRelayEntry
}

func newCallbackRelay() *callbackRelay {
	return &callbackRelay{entries: make(map[string]callbackRelayEntry)}
}

var defaultCallbackRelay = newCallbackRelay()

func (r *callbackRelay) pruneLocked(now time.Time) {
	for k, e := range r.entries {
		if now.Sub(e.at) > callbackRelayTTL {
			delete(r.entries, k)
		}
	}
}

func (r *callbackRelay) evictOldestLocked() {
	var oldestKey string
	var oldestAt time.Time
	for k, e := range r.entries {
		if oldestKey == "" || e.at.Before(oldestAt) {
			oldestKey = k
			oldestAt = e.at
		}
	}
	if oldestKey != "" {
		delete(r.entries, oldestKey)
	}
}

func (r *callbackRelay) record(state, raw, errCode, errDesc string) {
	if state == "" || (raw == "" && errCode == "") {
		return
	}
	if len(state) > callbackRelayMaxValueLen || len(raw) > callbackRelayMaxValueLen || len(errCode) > callbackRelayMaxValueLen || len(errDesc) > callbackRelayMaxValueLen {
		return
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	if _, exists := r.entries[state]; !exists && len(r.entries) >= callbackRelayMaxEntries {
		r.evictOldestLocked()
	}
	r.entries[state] = callbackRelayEntry{
		raw:       raw,
		errorCode: errCode,
		errorDesc: errDesc,
		at:        now,
	}
}

func (r *callbackRelay) take(state string) (callbackRelayEntry, bool) {
	if state == "" {
		return callbackRelayEntry{}, false
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	entry, ok := r.entries[state]
	if !ok {
		return callbackRelayEntry{}, false
	}
	delete(r.entries, state)
	return entry, true
}

// extractCallbackRaw mirrors web/src/lib/oauth-handoff.ts:parseCallbackURL
// so single-code redirects return the bare token while multi-param bundles
// preserve the full query string for per-provider parsers.
func extractCallbackRaw(q url.Values, rawQuery string) string {
	code := firstNonEmpty(q.Get("code"), q.Get("access_token"), q.Get("token"), q.Get("refreshToken"), q.Get("refresh_token"))
	extra := 0
	for k, vals := range q {
		val := firstNonEmpty(vals...)
		if val == "" {
			continue
		}
		switch k {
		case "state":
		case "code", "access_token", "token", "refreshToken", "refresh_token":
			if val != code {
				extra++
			}
		default:
			extra++
		}
	}
	if code != "" && extra == 0 {
		return code
	}
	return strings.TrimLeft(rawQuery, "?#&")
}

// callbackPage is a self-contained OAuth landing page. Providers redirect the
// user's browser here (e.g. /callback?code=... or /callback#access_token=...)
// after login; there is no server-side session, so the page extracts the
// values client-side and asks the user to paste them into the dashboard modal.
// Served without API key (browsers carry none). All DOM writes use
// textContent — query values are never injected as HTML (XSS-safe).
const callbackPage = `<!DOCTYPE html>
<html lang="id">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>9router — OAuth callback</title>
<style>
:root{color-scheme:dark}
body{background:#0b0e14;color:#e6e9f0;font-family:ui-sans-serif,system-ui,sans-serif;margin:0;padding:32px 16px}
.card{max-width:640px;margin:0 auto;background:#131722;border:1px solid #2a3040;border-radius:12px;padding:24px}
h1{font-size:18px;margin:0 0 4px}
.sub{font-size:13px;color:#9aa3b5;margin:0 0 16px}
.ok{color:#4ade80;font-weight:600}
.err{color:#f87171;font-weight:600}
textarea{width:100%;box-sizing:border-box;min-height:110px;background:#0b0e14;color:#e6e9f0;border:1px solid #2a3040;border-radius:8px;font:12px/1.5 ui-monospace,monospace;padding:10px;resize:vertical}
.row{display:flex;gap:8px;margin-top:10px}
button{flex:1;padding:10px;border-radius:8px;border:1px solid #2a3040;background:#1d2536;color:#e6e9f0;font-size:13px;font-weight:600;cursor:pointer}
button.primary{background:#2563eb;border-color:#2563eb}
button:hover{filter:brightness(1.15)}
.hint{font-size:12px;color:#9aa3b5;margin-top:12px}
.hint code{color:#c4b5fd}
ol{font-size:13px;color:#c6cddb;padding-left:20px;margin:12px 0 0}
ol li{margin:4px 0}
#meta{font-size:12px;color:#9aa3b5;margin-top:8px;word-break:break-all}
</style>
</head>
<body>
<div class="card">
<h1>9router — OAuth callback</h1>
<p class="sub" id="status">Memproses…</p>
<textarea id="code" readonly placeholder="(no code in URL)"></textarea>
<div class="row">
<button class="primary" id="copy">Copy</button>
<button id="copied-url">Copy full URL</button>
</div>
<div id="meta"></div>
<ol>
<li>Click Log in on the dashboard — the connection completes <b>automatically</b> and this tab closes itself.</li>
<li>If auto-submit fails: click <b>Copy</b>, paste into the callback field in the provider modal, then click <b>Connect</b>.</li>
<li>This tab can be closed once Connect succeeds.</li>
</ol>
<p class="hint">The token in this URL is a credential — do not share it with anyone.</p>
</div>
<script>
(function(){
  function $(id){return document.getElementById(id)}
  var statusEl=$("status"), codeEl=$("code"), metaEl=$("meta");
  var CB_KEY="9router.oauth.callback.v1", CB_CHANNEL="9router-oauth";
  var q=new URLSearchParams(location.search);
  var h=new URLSearchParams(location.hash.replace(/^#/,""));
  function pick(k){return q.get(k)||h.get(k)||""}
  var err=pick("error")||pick("error_code")||pick("errorCode");
  var errDesc=pick("error_description")||pick("error_desc")||pick("message");
  var code=pick("code")||pick("access_token")||pick("token")||pick("refreshToken")||pick("refresh_token");
  var state=pick("state");
  function setStatus(ok,msg){statusEl.textContent=msg;statusEl.className="sub "+(ok?"ok":"err")}
  function handOff(payload){
    try{localStorage.setItem(CB_KEY,JSON.stringify(payload))}catch(e){}
    try{var bc=new BroadcastChannel(CB_CHANNEL);bc.postMessage(payload);bc.close()}catch(e){}
    try{
      if(window.opener){
        var callbackData={code:code,state:state,error:err||"",errorDescription:errDesc||""};
        var origins=[location.origin,"http://localhost:1455"];
        var peerHost=location.hostname==="localhost"?"127.0.0.1":(location.hostname==="127.0.0.1"?"localhost":"");
        if(peerHost){origins.push(location.protocol+"//"+peerHost+(location.port?":"+location.port:""))}
        origins.forEach(function(origin){
          try{window.opener.postMessage({type:"oauth_callback",data:callbackData},origin)}catch(e){}
        });
      }
    }catch(e){}
  }
  if(err){
    setStatus(false,"Login failed: "+err+(errDesc?" — "+errDesc:""));
    handOff({state:state,raw:"",error:err,errorDesc:errDesc,at:Date.now()});
  }else if(code){
    setStatus(true,"Login successful! Sending to the dashboard — this tab closes automatically…");
    codeEl.value=code;
    var extra=[];
    if(state)extra.push("state: "+state);
    try{
      var padded=code.replace(/-/g,"+").replace(/_/g,"/");
      while(padded.length%4)padded+="=";
      var obj=JSON.parse(atob(padded));
      if(obj&&obj.accessToken){
        extra.push("terdeteksi token Cline"+(obj.email?" ("+obj.email+")":""));
      }
    }catch(e){}
    handOff({state:state,raw:code,at:Date.now()});
    // Auto-handoff: the dashboard tab auto-submits; this tab then closes (only
    // reliable when it was opened via window.open — otherwise the user closes it).
    var n=3;
    metaEl.textContent=(extra.length?extra.join(" · ")+" · ":"")+"Closing in "+n+"…";
    var timer=setInterval(function(){
      n-=1;
      if(n<=0){
        clearInterval(timer);
        try{window.close()}catch(e){}
        metaEl.textContent=(extra.length?extra.join(" · ")+" · ":"")+"Koneksi diproses di tab dashboard — tab ini boleh ditutup.";
      }else{
        metaEl.textContent=(extra.length?extra.join(" · ")+" · ":"")+"Closing in "+n+"…";
      }
    },1000);
  }else{
    setStatus(false,"No code in this URL. Repeat the login from the dashboard and make sure you paste the full URL.");
  }
  function flash(btn,txt){var o=btn.textContent;btn.textContent=txt;setTimeout(function(){btn.textContent=o},1500)}
  $("copy").addEventListener("click",function(){
    if(!codeEl.value)return;
    navigator.clipboard.writeText(codeEl.value).then(
      function(){flash($("copy"),"Tersalin!")},
      function(){codeEl.select();document.execCommand("copy");flash($("copy"),"Tersalin!")});
  });
  $("copied-url").addEventListener("click",function(){
    navigator.clipboard.writeText(location.href).then(
      function(){flash($("copied-url"),"URL tersalin!")},
      function(){flash($("copied-url"),"Gagal — copy manual dari address bar")});
  });
})();
</script>
</body>
</html>`

// HandleCallbackPage serves the public OAuth landing page.
// GET /callback — no auth required (provider redirects carry no API key).
func (h *OAuthHandler) HandleCallbackPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		state := q.Get("state")
		errCode := firstNonEmpty(q.Get("error"), q.Get("error_code"), q.Get("errorCode"))
		errDesc := firstNonEmpty(q.Get("error_description"), q.Get("error_desc"), q.Get("message"))
		var raw string
		if errCode == "" {
			raw = extractCallbackRaw(q, r.URL.RawQuery)
		}
		if state != "" && (errCode != "" || raw != "") {
			h.callbackStore().record(state, raw, errCode, errDesc)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write([]byte(callbackPage))
}

// HandleCallbackResult pops a server-recorded /callback payload for an OAuth
// state so cross-origin dashboard tabs (e.g. 127.0.0.1 vs localhost) still
// receive loopback callbacks even when storage and window.opener are isolated.
// GET /api/oauth/callback-result?state=...
func (h *OAuthHandler) HandleCallbackResult(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing state")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	entry, ok := h.callbackStore().take(state)
	if !ok {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"found":     true,
		"state":     state,
		"raw":       entry.raw,
		"error":     entry.errorCode,
		"errorDesc": entry.errorDesc,
	})
}
