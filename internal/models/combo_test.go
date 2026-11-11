package models_test

import (
	json "encoding/json/v2"
	"reflect"
	"testing"

	"9router/proxy/internal/models"
)

func TestComboModel_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    models.ComboModel
		wantErr bool
	}{
		{
			name:  "plain string",
			input: `"openai/gpt-4o"`,
			want:  models.ComboModel{Model: "openai/gpt-4o", Weight: 1},
		},
		{
			name:  "object with model and weight",
			input: `{"model":"openai/gpt-4o","weight":3}`,
			want:  models.ComboModel{Model: "openai/gpt-4o", Weight: 3},
		},
		{
			name:  "object with weight unset defaults to 1",
			input: `{"model":"anthropic/claude-3-5-sonnet"}`,
			want:  models.ComboModel{Model: "anthropic/claude-3-5-sonnet", Weight: 1},
		},
		{
			name:  "object with zero weight",
			input: `{"model":"deepseek/chat","weight":0}`,
			want:  models.ComboModel{Model: "deepseek/chat", Weight: 0},
		},
		{
			name:  "object with fullModel",
			input: `{"fullModel":"openai/gpt-4o","weight":2}`,
			want:  models.ComboModel{Model: "openai/gpt-4o", Weight: 2},
		},
		{
			name:  "object with negative weight clamped to 0",
			input: `{"model":"m1","weight":-5}`,
			want:  models.ComboModel{Model: "m1", Weight: 0},
		},
		{
			name:  "object with weight above 1000 clamped to 1000",
			input: `{"model":"m1","weight":9999}`,
			want:  models.ComboModel{Model: "m1", Weight: 1000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got models.ComboModel
			err := json.Unmarshal([]byte(tt.input), &got)
			if (err != nil) != tt.wantErr {
				t.Fatalf("UnmarshalJSON error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCombo_ParsedModels(t *testing.T) {
	c := &models.Combo{
		Models: `[{"model":"fast","weight":3},"default",{"model":"disabled","weight":0}]`,
	}

	cms := c.ParsedModels()
	if len(cms) != 3 {
		t.Fatalf("expected 3 models, got %d", len(cms))
	}
	if cms[0].Model != "fast" || cms[0].Weight != 3 {
		t.Errorf("cms[0] = %+v", cms[0])
	}
	if cms[1].Model != "default" || cms[1].Weight != 1 {
		t.Errorf("cms[1] = %+v", cms[1])
	}
	if cms[2].Model != "disabled" || cms[2].Weight != 0 {
		t.Errorf("cms[2] = %+v", cms[2])
	}

	names := c.ModelNames()
	wantNames := []string{"fast", "default", "disabled"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("names = %v, want %v", names, wantNames)
	}

	weights := c.ModelWeights()
	wantWeights := map[string]int{"fast": 3, "default": 1, "disabled": 0}
	if !reflect.DeepEqual(weights, wantWeights) {
		t.Errorf("weights = %v, want %v", weights, wantWeights)
	}
}
