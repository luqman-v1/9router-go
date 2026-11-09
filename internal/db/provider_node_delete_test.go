package db

import (
	"testing"
)

func TestDeleteProviderNode(t *testing.T) {
	tests := []struct {
		name           string
		setupNodes     []string          // node IDs to create
		setupConns     []string          // provider IDs for connections to create
		setupKV        map[string]string // key -> value in scope 'customModels'
		setupTrigger   string            // optional SQL trigger to install before delete
		deleteID       string
		wantErr        bool     // whether DeleteProviderNode is expected to fail
		wantNodeGone   []string // node IDs that should not exist
		wantNodeExist  []string // node IDs that should still exist
		wantConnsGone  []string // provider IDs whose connections should be deleted
		wantConnsExist []string // provider IDs whose connections should still exist
		wantKVExist    []string // KV keys that should still exist
		wantKVGone     []string // KV keys that should be deleted
	}{
		{
			name:       "pipe separated custom models pruned for target node while other alias survives",
			setupNodes: []string{"node-a", "node-b"},
			setupConns: []string{"node-a", "node-b"},
			setupKV: map[string]string{
				"node-a|gpt-4o|chat":   `{"providerAlias":"node-a","modelId":"gpt-4o"}`,
				"node-b|claude-3|chat": `{"providerAlias":"node-b","modelId":"claude-3"}`,
			},
			deleteID:       "node-a",
			wantNodeGone:   []string{"node-a"},
			wantConnsGone:  []string{"node-a"},
			wantConnsExist: []string{"node-b"},
			wantKVExist:    []string{"node-b|claude-3|chat"},
			wantKVGone:     []string{"node-a|gpt-4o|chat"},
		},
		{
			name:       "slash separated custom models pruned",
			setupNodes: []string{"node-a"},
			setupKV: map[string]string{
				"node-a/my-custom-model": `{"providerAlias":"node-a","modelId":"my-custom-model"}`,
				"node-b/other-model":     `{"providerAlias":"node-b","modelId":"other-model"}`,
			},
			deleteID:     "node-a",
			wantNodeGone: []string{"node-a"},
			wantKVExist:  []string{"node-b/other-model"},
			wantKVGone:   []string{"node-a/my-custom-model"},
		},
		{
			name:       "unparseable key naming target node in value is pruned",
			setupNodes: []string{"node-a"},
			setupKV: map[string]string{
				"node-a":            `{"providerAlias":"node-a"}`,
				"node-a|valid|chat": `{"providerAlias":"node-a","modelId":"valid"}`,
			},
			deleteID:     "node-a",
			wantNodeGone: []string{"node-a"},
			wantKVGone:   []string{"node-a", "node-a|valid|chat"},
		},
		{
			name:       "unparseable key naming different node in value survives",
			setupNodes: []string{"node-a", "node-b"},
			setupKV: map[string]string{
				"unparseable-node": `{"providerAlias":"node-b"}`,
			},
			deleteID:     "node-a",
			wantNodeGone: []string{"node-a"},
			wantKVExist:  []string{"unparseable-node"},
		},
		{
			name:       "unknown node id is no-op and prunes nothing",
			setupNodes: []string{"node-b"},
			setupKV: map[string]string{
				"node-unknown|ghost|chat":  `{"providerAlias":"node-unknown","modelId":"ghost"}`,
				"node-unknown/slash-model": `{"providerAlias":"node-unknown","modelId":"slash-model"}`,
			},
			deleteID:     "node-unknown",
			wantNodeGone: nil,
			wantKVExist:  []string{"node-unknown|ghost|chat", "node-unknown/slash-model"},
			wantKVGone:   nil,
		},
		{
			name:       "orphan custom model whose value names target node is pruned even when key alias differs",
			setupNodes: []string{"node-a", "node-b"},
			setupKV: map[string]string{
				"node-b|disputed|chat": `{"providerAlias":"node-a","modelId":"disputed"}`,
			},
			deleteID:     "node-a",
			wantNodeGone: []string{"node-a"},
			wantKVGone:   []string{"node-b|disputed|chat"},
		},
		{
			name:       "row whose key and value both name a different node is left intact",
			setupNodes: []string{"node-a", "node-b"},
			setupKV: map[string]string{
				"node-b|other|chat": `{"providerAlias":"node-b","modelId":"other"}`,
			},
			deleteID:     "node-a",
			wantNodeGone: []string{"node-a"},
			wantKVExist:  []string{"node-b|other|chat"},
			wantKVGone:   nil,
		},
		{
			name:       "invalid JSON value does not break prune and key match still prunes while other survives",
			setupNodes: []string{"node-a", "node-b"},
			setupKV: map[string]string{
				"node-a|broken|chat":   `{not-valid-json`,
				"node-b|survivor|chat": `{not-valid-json`,
			},
			deleteID:     "node-a",
			wantNodeGone: []string{"node-a"},
			wantKVExist:  []string{"node-b|survivor|chat"},
			wantKVGone:   []string{"node-a|broken|chat"},
		},
		{
			name:           "failing providerConnections delete rolls back node delete and returns error",
			setupNodes:     []string{"node-a"},
			setupConns:     []string{"node-a"},
			setupTrigger:   "CREATE TRIGGER fail_conn_delete BEFORE DELETE ON providerConnections BEGIN SELECT RAISE(ABORT, 'forced connection delete failure'); END;",
			deleteID:       "node-a",
			wantErr:        true,
			wantNodeExist:  []string{"node-a"},
			wantConnsExist: []string{"node-a"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := openEphemeralSchemaDB(t)

			for _, nodeID := range tc.setupNodes {
				if _, err := repo.CreateProviderNode(nodeID, "openai-compatible", "Test "+nodeID,
					`{"prefix":"`+nodeID+`","apiType":"openai-compatible","baseUrl":"https://example.test"}`); err != nil {
					t.Fatalf("setup node %s: %v", nodeID, err)
				}
			}

			for _, connProv := range tc.setupConns {
				connID := "conn-" + connProv
				if _, err := repo.RawDB().Exec(
					`INSERT INTO providerConnections (id, provider, authType, data, createdAt, updatedAt) VALUES (?, ?, 'api-key', '{}', '2026-01-01', '2026-01-01')`,
					connID, connProv,
				); err != nil {
					t.Fatalf("setup conn %s: %v", connID, err)
				}
			}

			for k, v := range tc.setupKV {
				if _, err := repo.RawDB().Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`, k, v); err != nil {
					t.Fatalf("setup kv %s: %v", k, err)
				}
			}

			if tc.setupTrigger != "" {
				if _, err := repo.RawDB().Exec(tc.setupTrigger); err != nil {
					t.Fatalf("setup trigger: %v", err)
				}
			}

			err := repo.DeleteProviderNode(t.Context(), tc.deleteID)
			if (err != nil) != tc.wantErr {
				t.Fatalf("DeleteProviderNode(%q) error = %v, wantErr %v", tc.deleteID, err, tc.wantErr)
			}

			for _, nodeID := range tc.wantNodeGone {
				node, _, err := repo.GetProviderNodeByID(nodeID)
				if err != nil {
					t.Errorf("GetProviderNodeByID(%q) error = %v", nodeID, err)
				}
				if node != nil {
					t.Errorf("node %q still exists, expected deleted", nodeID)
				}
			}

			for _, nodeID := range tc.wantNodeExist {
				node, _, err := repo.GetProviderNodeByID(nodeID)
				if err != nil {
					t.Errorf("GetProviderNodeByID(%q) error = %v", nodeID, err)
				}
				if node == nil {
					t.Errorf("node %q expected to exist, got nil", nodeID)
				}
			}

			for _, connProv := range tc.wantConnsGone {
				connID := "conn-" + connProv
				var count int
				if err := repo.RawDB().QueryRow(`SELECT COUNT(1) FROM providerConnections WHERE id = ?`, connID).Scan(&count); err != nil {
					t.Fatalf("query conn %s: %v", connID, err)
				}
				if count != 0 {
					t.Errorf("connection %q expected to be deleted, count = %d", connID, count)
				}
			}

			for _, connProv := range tc.wantConnsExist {
				connID := "conn-" + connProv
				var count int
				if err := repo.RawDB().QueryRow(`SELECT COUNT(1) FROM providerConnections WHERE id = ?`, connID).Scan(&count); err != nil {
					t.Fatalf("query conn %s: %v", connID, err)
				}
				if count != 1 {
					t.Errorf("connection %q expected to exist, count = %d", connID, count)
				}
			}

			for _, k := range tc.wantKVExist {
				var count int
				if err := repo.RawDB().QueryRow(`SELECT COUNT(1) FROM kv WHERE scope = 'customModels' AND key = ?`, k).Scan(&count); err != nil {
					t.Fatalf("query kv %s: %v", k, err)
				}
				if count != 1 {
					t.Errorf("kv row %q expected to exist, count = %d", k, count)
				}
			}

			for _, k := range tc.wantKVGone {
				var count int
				if err := repo.RawDB().QueryRow(`SELECT COUNT(1) FROM kv WHERE scope = 'customModels' AND key = ?`, k).Scan(&count); err != nil {
					t.Fatalf("query kv %s: %v", k, err)
				}
				if count != 0 {
					t.Errorf("kv row %q expected to be pruned, count = %d", k, count)
				}
			}
		})
	}
}
