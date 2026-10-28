package browserlogin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// fakeCDP is a stand-in for Chrome's DevTools endpoint. It records the
// expression it is handed and answers with the value the test decides.
type fakeCDP struct {
	srv      *httptest.Server
	expr     string
	value    string
	upgrader websocket.Upgrader
}

func newFakeCDP(t *testing.T, value string) *fakeCDP {
	t.Helper()
	c := &fakeCDP{value: value}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := c.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req struct {
				ID     int `json:"id"`
				Params struct {
					Expression string `json:"expression"`
				} `json:"params"`
			}
			if err := json.Unmarshal(msg, &req); err != nil {
				continue
			}
			c.expr = req.Params.Expression
			reply, _ := json.Marshal(map[string]any{
				"id": req.ID,
				"result": map[string]any{
					"type":  "string",
					"value": c.value,
				},
			})
			if err := conn.WriteMessage(websocket.TextMessage, reply); err != nil {
				return
			}
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *fakeCDP) wsURL() string {
	return "ws://" + strings.TrimPrefix(c.srv.URL, "http://")
}

// TestReadDeepSeekUserTokenSendsJavaScriptSource pins the wire contract:
// Runtime.evaluate evaluates what it is given as JavaScript, so the
// expression must be source, not a JSON string literal.
//
// A marshalled expression reaches Chrome as a quoted string literal, which
// JavaScript evaluates to the literal's own text — so the credential read
// back is the text of the expression rather than the token. That is the
// defect this test exists to prevent.
func TestReadDeepSeekUserTokenSendsJavaScriptSource(t *testing.T) {
	c := newFakeCDP(t, "THE-REAL-TOKEN")

	token, ok := readDeepSeekUserToken(context.Background(), c.wsURL())
	if !ok {
		t.Fatalf("the credential was not read: token=%q", token)
	}
	if token != "THE-REAL-TOKEN" {
		t.Errorf("token = %q, want %q", token, "THE-REAL-TOKEN")
	}

	if c.expr == "" {
		t.Fatal("no expression reached the browser")
	}
	// The expression must be the source itself.
	if c.expr != `localStorage.getItem("userToken")` {
		t.Errorf("expression = %q, want the JavaScript source", c.expr)
	}
	// And it must not be a JSON string literal, which JavaScript would
	// evaluate to its own text.
	var asLiteral string
	if err := json.Unmarshal([]byte(c.expr), &asLiteral); err == nil {
		t.Errorf("the expression is a JSON string literal (%q): Chrome would evaluate it to its own text", asLiteral)
	}
}
