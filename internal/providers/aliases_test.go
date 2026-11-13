package providers

import (
	"reflect"
	"testing"
)

func TestResolveModelAlias_DeepSeekV4Pro(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		want   ModelAliasResolution
		wantOk bool
	}{
		{
			name:   "max alias resolves to base model with max effort",
			model:  "deepseek-v4-pro-max",
			want:   ModelAliasResolution{Model: "deepseek-v4-pro", ThinkingType: "enabled", ReasoningEffort: "max"},
			wantOk: true,
		},
		{
			name:   "none alias resolves to base model with thinking disabled",
			model:  "deepseek-v4-pro-none",
			want:   ModelAliasResolution{Model: "deepseek-v4-pro", ThinkingType: "disabled", ReasoningEffort: ""},
			wantOk: true,
		},
		{
			name:   "base model is not an alias",
			model:  "deepseek-v4-pro",
			want:   ModelAliasResolution{},
			wantOk: false,
		},
		{
			name:   "other deepseek model is not an alias",
			model:  "deepseek-v4-flash",
			want:   ModelAliasResolution{},
			wantOk: false,
		},
		{
			name:   "empty model is not an alias",
			model:  "",
			want:   ModelAliasResolution{},
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveModelAlias(tt.model)
			if ok != tt.wantOk {
				t.Errorf("ResolveModelAlias(%q) ok = %v, want %v", tt.model, ok, tt.wantOk)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ResolveModelAlias(%q) = %+v, want %+v", tt.model, got, tt.want)
			}
		})
	}
}
