package chat

import (
	"context"
	"errors"
	"testing"
)

func TestParityUpstreamCancelIsNotClientAbort(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded,
		errors.New("upstream: client closed"), errors.New("upstream: broken pipe")} {
		if isClientCanceled(context.Background(), err) {
			t.Fatalf("misclassified %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !isClientCanceled(ctx, context.Canceled) {
		t.Fatal("client abort not detected")
	}
}
