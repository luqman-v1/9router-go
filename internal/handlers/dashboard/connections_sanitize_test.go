package dashboard

import (
	"testing"

	"9router/proxy/internal/models"
)

func TestSanitizeProviderConnection_LastErrorAttribution(t *testing.T) {
	tests := []struct {
		name               string
		jsonData           string
		wantLastError      string
		wantLastErrorModel any
		wantLastErrorSource any
	}{
		{
			name:               "map lastError with model and source emits both",
			jsonData:           `{"lastError":{"message":"Model is not supported","status":401,"model":"mimo-v2.5-free","source":"chat"}}`,
			wantLastError:      "Model is not supported",
			wantLastErrorModel: "mimo-v2.5-free",
			wantLastErrorSource: "chat",
		},
		{
			name:               "map lastError without model leaves lastErrorModel absent",
			jsonData:           `{"lastError":{"message":"Invalid API key","status":401}}`,
			wantLastError:      "Invalid API key",
			wantLastErrorModel: nil,
			wantLastErrorSource: nil,
		},
		{
			name:               "string lastError from probe leaves lastErrorModel absent",
			jsonData:           `{"lastError":"Connection timed out"}`,
			wantLastError:      "Connection timed out",
			wantLastErrorModel: nil,
			wantLastErrorSource: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &models.ProviderConnection{
				ID:       "test-conn",
				Provider: "openrouter",
				AuthType: "api_key",
				IsActive: 1,
				Data:     tt.jsonData,
			}
			sanitized := sanitizeProviderConnection(conn)

			if got := sanitized["lastError"]; got != tt.wantLastError {
				t.Errorf("lastError = %v, want %v", got, tt.wantLastError)
			}
			if got := sanitized["lastErrorModel"]; got != tt.wantLastErrorModel {
				t.Errorf("lastErrorModel = %v, want %v", got, tt.wantLastErrorModel)
			}
			if got := sanitized["lastErrorSource"]; got != tt.wantLastErrorSource {
				t.Errorf("lastErrorSource = %v, want %v", got, tt.wantLastErrorSource)
			}
		})
	}
}
