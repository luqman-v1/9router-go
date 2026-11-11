package providers

import (
	"testing"
	"time"
)

func TestGetEscalatingLockoutCooldown(t *testing.T) {
	tests := []struct {
		name   string
		streak int
		want   time.Duration
	}{
		{"zero streak means no lockout", 0, 0},
		{"negative streak means no lockout", -3, 0},
		{"first lockout is 24 hours", 1, 24 * time.Hour},
		{"second lockout is 48 hours", 2, 48 * time.Hour},
		{"third lockout is 4 days", 3, 96 * time.Hour},
		{"fourth lockout is 7 days", 4, 168 * time.Hour},
		{"fifth lockout is 14 days", 5, 336 * time.Hour},
		{"sixth lockout is also 14 days", 6, 336 * time.Hour},
		{"tenth lockout is capped at 14 days", 10, 336 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetEscalatingLockoutCooldown(tt.streak); got != tt.want {
				t.Errorf("GetEscalatingLockoutCooldown(%d) = %s, want %s", tt.streak, got, tt.want)
			}
		})
	}
}
