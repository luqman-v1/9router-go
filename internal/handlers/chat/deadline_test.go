package chat

import (
	"context"
	"testing"
	"time"
)

func TestWithTotalRequestDeadline(t *testing.T) {
	t.Run("zero is off", func(t *testing.T) {
		ctx, cancel := WithTotalRequestDeadline(context.Background(), 0)
		defer cancel()
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("a zero bound set a deadline")
		}
		if isRequestDeadline(ctx) {
			t.Fatal("a zero bound marked the context as gateway-bounded")
		}
	})

	t.Run("negative is off", func(t *testing.T) {
		ctx, cancel := WithTotalRequestDeadline(context.Background(), -time.Second)
		defer cancel()
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("a negative bound set a deadline")
		}
	})

	t.Run("positive bound marks the context", func(t *testing.T) {
		ctx, cancel := WithTotalRequestDeadline(context.Background(), time.Minute)
		defer cancel()
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("the bound set no deadline")
		}
		if remaining := time.Until(deadline); remaining > time.Minute || remaining < 50*time.Second {
			t.Fatalf("deadline is %v away, want about a minute", remaining)
		}
		if !isRequestDeadline(ctx) {
			t.Fatal("the bound did not mark the context")
		}
	})

	t.Run("a tighter existing deadline is kept", func(t *testing.T) {
		base, baseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer baseCancel()

		ctx, cancel := WithTotalRequestDeadline(base, time.Hour)
		defer cancel()
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("the kept deadline vanished")
		}
		if remaining := time.Until(deadline); remaining > 6*time.Second {
			t.Fatalf("deadline is %v away, want the caller's 5s to win", remaining)
		}
		if isRequestDeadline(ctx) {
			t.Fatal("a kept deadline was relabelled as the gateway's own")
		}
	})
}

func TestTotalRequestTimeoutReadsSetting(t *testing.T) {
	repo := newTestRepo(t)
	h := NewChatHandler(repo)

	if got := h.totalRequestTimeout(context.Background()); got != 0 {
		t.Fatalf("unset setting resolved to %v, want off", got)
	}

	if err := repo.UpdateSettingsRaw(map[string]any{TotalRequestTimeoutSetting: 1500}); err != nil {
		t.Fatal(err)
	}
	if got := h.totalRequestTimeout(context.Background()); got != 1500*time.Millisecond {
		t.Fatalf("resolved %v, want 1.5s", got)
	}

	if err := repo.UpdateSettingsRaw(map[string]any{TotalRequestTimeoutSetting: 0}); err != nil {
		t.Fatal(err)
	}
	if got := h.totalRequestTimeout(context.Background()); got != 0 {
		t.Fatalf("an explicit zero resolved to %v, want off", got)
	}
}
