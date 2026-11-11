package media

import (
	"context"
	"testing"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
)

func TestSelectMediaConnections(t *testing.T) {
	database, cleanup := setupMediaTestDB(t)
	defer cleanup()

	mediaH := NewMediaHandler(database, nil, nil)

	p1 := 1
	p2 := 2
	p3 := 3

	// conn1: chat only (mediaEnabled: false)
	if err := database.CreateProviderConnectionFull("conn-text-only", "openai", "apikey", "Text Only", &p1, `{"apiKey":"sk-1","mediaEnabled":false}`); err != nil {
		t.Fatalf("failed to create conn1: %v", err)
	}

	// conn2: media enabled
	if err := database.CreateProviderConnectionFull("conn-media-only", "openai", "apikey", "Media Only", &p2, `{"apiKey":"sk-2","mediaEnabled":true}`); err != nil {
		t.Fatalf("failed to create conn2: %v", err)
	}

	// conn3: image disabled via disabledKinds
	if err := database.CreateProviderConnectionFull("conn-image-disabled", "openai", "apikey", "Video Only", &p3, `{"apiKey":"sk-3","providerSpecificData":{"disabledKinds":["image"]}}`); err != nil {
		t.Fatalf("failed to create conn3: %v", err)
	}

	t.Run("image candidate pool excludes mediaEnabled false and disabledKinds image", func(t *testing.T) {
		conns, err := mediaH.SelectMediaConnections("openai", "image")
		if err != nil {
			t.Fatalf("SelectMediaConnections failed: %v", err)
		}
		if len(conns) != 1 {
			t.Fatalf("expected 1 connection for image, got %d", len(conns))
		}
		if conns[0].ID != "conn-media-only" {
			t.Errorf("expected conn-media-only, got %s", conns[0].ID)
		}
	})

	t.Run("video candidate pool includes disabledKinds image connection", func(t *testing.T) {
		conns, err := mediaH.SelectMediaConnections("openai", "video")
		if err != nil {
			t.Fatalf("SelectMediaConnections failed: %v", err)
		}
		if len(conns) != 2 {
			t.Fatalf("expected 2 connections for video, got %d", len(conns))
		}
		ids := map[string]bool{conns[0].ID: true, conns[1].ID: true}
		if !ids["conn-media-only"] || !ids["conn-image-disabled"] {
			t.Errorf("expected conn-media-only and conn-image-disabled, got %+v", ids)
		}
		if ids["conn-text-only"] {
			t.Errorf("conn-text-only should not be in video pool")
		}
	})
}

func TestMediaPoolVsChatRouting(t *testing.T) {
	database, cleanup := setupMediaTestDB(t)
	defer cleanup()

	chatH := chat.NewChatHandler(database, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	p1 := 1
	p2 := 2

	// conn-text: priority 1, mediaEnabled: false
	if err := database.CreateProviderConnectionFull("conn-text-only", "custom-media-prov", "apikey", "Text Conn", &p1, `{"apiKey":"sk-text","mediaEnabled":false}`); err != nil {
		t.Fatalf("failed to create conn-text: %v", err)
	}

	// conn-media: priority 2, mediaEnabled: true
	if err := database.CreateProviderConnectionFull("conn-media-only", "custom-media-prov", "apikey", "Media Conn", &p2, `{"apiKey":"sk-media","mediaEnabled":true}`); err != nil {
		t.Fatalf("failed to create conn-media: %v", err)
	}

	t.Run("image request skips conn-text-only and routes to conn-media-only", func(t *testing.T) {
		imageCtx := handlerutil.WithServiceKind(context.Background(), "image")
		conn, data, err := chatH.GetBestConnectionWithContext(imageCtx, "custom-media-prov", "", nil, "")
		if err != nil {
			t.Fatalf("GetBestConnectionWithContext failed: %v", err)
		}
		if conn.ID != "conn-media-only" {
			t.Errorf("expected conn-media-only for image, got %s", conn.ID)
		}
		if data.APIKey != "sk-media" {
			t.Errorf("expected sk-media, got %s", data.APIKey)
		}
	})

	t.Run("video request skips conn-text-only and routes to conn-media-only", func(t *testing.T) {
		videoCtx := handlerutil.WithServiceKind(context.Background(), "video")
		conn, _, err := chatH.GetBestConnectionWithContext(videoCtx, "custom-media-prov", "", nil, "")
		if err != nil {
			t.Fatalf("GetBestConnectionWithContext failed: %v", err)
		}
		if conn.ID != "conn-media-only" {
			t.Errorf("expected conn-media-only for video, got %s", conn.ID)
		}
	})

	t.Run("chat request keeps conn-text-only eligible and picks it due to higher priority", func(t *testing.T) {
		// Plain context has no serviceKind (representing text chat completions)
		chatCtx := context.Background()
		conn, data, err := chatH.GetBestConnectionWithContext(chatCtx, "custom-media-prov", "", nil, "")
		if err != nil {
			t.Fatalf("GetBestConnectionWithContext failed: %v", err)
		}
		if conn.ID != "conn-text-only" {
			t.Errorf("expected conn-text-only for chat (priority 1), got %s", conn.ID)
		}
		if data.APIKey != "sk-text" {
			t.Errorf("expected sk-text, got %s", data.APIKey)
		}
	})

	t.Run("pinned text connection is ineligible for media but eligible for chat", func(t *testing.T) {
		imageCtx := handlerutil.WithServiceKind(context.Background(), "image")
		// Pinned to conn-text-only: should fall back to strategy (conn-media-only)
		conn, _, err := chatH.GetBestConnectionWithContext(imageCtx, "custom-media-prov", "conn-text-only", nil, "")
		if err != nil {
			t.Fatalf("expected fallback to work, got err: %v", err)
		}
		if conn.ID != "conn-media-only" {
			t.Errorf("expected fallback to conn-media-only, got %s", conn.ID)
		}

		// Pinned to conn-text-only in chat: honors pin
		chatCtx := context.Background()
		connChat, _, errChat := chatH.GetBestConnectionWithContext(chatCtx, "custom-media-prov", "conn-text-only", nil, "")
		if errChat != nil {
			t.Fatalf("expected pin to succeed in chat, got err: %v", errChat)
		}
		if connChat.ID != "conn-text-only" {
			t.Errorf("expected conn-text-only for pinned chat, got %s", connChat.ID)
		}
	})

	t.Run("provider with only media-disabled connections returns no active connections for media", func(t *testing.T) {
		pOnly := 1
		if err := database.CreateProviderConnectionFull("conn-chat-only-provider", "text-only-provider", "apikey", "Chat Only", &pOnly, `{"apiKey":"sk-alone","mediaEnabled":false}`); err != nil {
			t.Fatalf("failed to create connection: %v", err)
		}

		imageCtx := handlerutil.WithServiceKind(context.Background(), "image")
		_, _, err := chatH.GetBestConnectionWithContext(imageCtx, "text-only-provider", "", nil, "")
		if err == nil {
			t.Fatalf("expected error for media request on text-only provider, got nil")
		}

		// Same provider succeeds for chat request
		conn, _, err := chatH.GetBestConnectionWithContext(context.Background(), "text-only-provider", "", nil, "")
		if err != nil {
			t.Fatalf("expected chat request to succeed, got error: %v", err)
		}
		if conn.ID != "conn-chat-only-provider" {
			t.Errorf("expected conn-chat-only-provider, got %s", conn.ID)
		}
	})
}

func TestIsConnectionMediaActiveHelper(t *testing.T) {
	tests := []struct {
		name        string
		dataJSON    string
		serviceKind string
		wantActive  bool
	}{
		{"empty string defaults true", "", "image", true},
		{"empty json defaults true", "{}", "image", true},
		{"top-level mediaEnabled true", `{"mediaEnabled":true}`, "image", true},
		{"top-level mediaEnabled false", `{"mediaEnabled":false}`, "image", false},
		{"top-level isMediaActive false", `{"isMediaActive":false}`, "image", false},
		{"psd mediaEnabled false", `{"providerSpecificData":{"mediaEnabled":false}}`, "image", false},
		{"psd isMediaActive false", `{"providerSpecificData":{"isMediaActive":false}}`, "image", false},
		{"disabledKinds contains kind", `{"providerSpecificData":{"disabledKinds":["image"]}}`, "image", false},
		{"disabledKinds other kind", `{"providerSpecificData":{"disabledKinds":["image"]}}`, "video", true},
		{"disabledKinds media keyword disables all", `{"providerSpecificData":{"disabledKinds":["media"]}}`, "video", false},
		{"disabledKinds media keyword without kind", `{"providerSpecificData":{"disabledKinds":["media"]}}`, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := models.IsConnectionMediaActive(tc.dataJSON, tc.serviceKind)
			if got != tc.wantActive {
				t.Errorf("IsConnectionMediaActive(%s, %q) = %v, want %v", tc.dataJSON, tc.serviceKind, got, tc.wantActive)
			}
		})
	}
}
