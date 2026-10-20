//go:build ignore

package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMockStreamComesFromJSONBody(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    bool
		wantErr bool
	}{
		{"stream", `{"stream":true}`, true, false},
		{"json", `{"stream":false}`, false, false},
		{"malformed", `{`, false, true},
		{"trailing", `{"stream":true}{}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body))
			got, err := mockWantsStream(httptest.NewRecorder(), request)
			if (err != nil) != tc.wantErr || (!tc.wantErr && got != tc.want) {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}
