package executor

import (
	"context"
	"testing"
)

func TestRequest_Release(t *testing.T) {
	req := &Request{
		Ctx:         context.Background(),
		Body:        []byte("hello"),
		ConnData:    map[string]any{"k": "v"},
		ToolNameMap: map[string]string{"a": "b"},
	}
	req.Release()
	if req.Body != nil {
		t.Errorf("Body not cleared: %v", req.Body)
	}
	if req.ConnData != nil {
		t.Errorf("ConnData not cleared: %v", req.ConnData)
	}
	if req.ToolNameMap != nil {
		t.Errorf("ToolNameMap not cleared: %v", req.ToolNameMap)
	}
	// nil safety
	var nilReq *Request
	nilReq.Release()
}
