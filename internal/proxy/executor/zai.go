package executor

import (
	"net/http"
)

// ForwardZai forwards to Z.ai (z.ai / bigmodel.cn) without touching the
// body: the unified thinking pass (internal/thinking, applied at the chat
// dispatch choke point) already rewrote the client's thinking request into
// the zai dialect before the body reached this executor. Keeping the
// registration for the glm/glm-cn lanes so those providers keep their exact
// transport path.
func ForwardZai(w http.ResponseWriter, req *Request) error {
	return ForwardOpenAI(w, req)
}
