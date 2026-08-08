package shutdown

import "testing"

func TestCancelClosesDone(t *testing.T) {
	if Fired() {
		t.Fatal("should not be fired initially")
	}
	Cancel()
	Cancel() // idempotent — must not panic on double close
	if !Fired() {
		t.Fatal("should be fired after Cancel")
	}
	select {
	case <-Done():
	default:
		t.Fatal("Done() should be closed after Cancel")
	}
}
