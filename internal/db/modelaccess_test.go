package db

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestModelAccess_EmptyMeansAllowAll(t *testing.T) {
	repo := openEphemeralSchemaDB(t)

	got, err := repo.GetAllowedModels("key-without-allowlist")
	if err != nil {
		t.Fatalf("GetAllowedModels: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected an empty allowlist for an unconfigured key, got %v", got)
	}

	// Setting then clearing restores the allow-everything default.
	if err := repo.SetAllowedModels("key-1", []string{"gpt-4o"}); err != nil {
		t.Fatalf("SetAllowedModels: %v", err)
	}
	if err := repo.SetAllowedModels("key-1", nil); err != nil {
		t.Fatalf("SetAllowedModels (clear): %v", err)
	}
	got, err = repo.GetAllowedModels("key-1")
	if err != nil {
		t.Fatalf("GetAllowedModels after clear: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("clearing must leave an empty allowlist, got %v", got)
	}
}

func TestModelAccess_SetAllowedModelsReplacesAndDedupes(t *testing.T) {
	repo := openEphemeralSchemaDB(t)

	if err := repo.SetAllowedModels("key-1", []string{"a", "b", "c"}); err != nil {
		t.Fatalf("first set: %v", err)
	}
	if err := repo.SetAllowedModels("key-1", []string{"c", "c", " ", "d"}); err != nil {
		t.Fatalf("second set: %v", err)
	}

	got, err := repo.GetAllowedModels("key-1")
	if err != nil {
		t.Fatalf("GetAllowedModels: %v", err)
	}
	want := []string{"c", "d"}
	if len(got) != len(want) {
		t.Fatalf("expected full replace to leave %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestModelAccess_IsolatedPerKey(t *testing.T) {
	repo := openEphemeralSchemaDB(t)

	if err := repo.SetAllowedModels("key-1", []string{"gpt-4o"}); err != nil {
		t.Fatalf("set key-1: %v", err)
	}

	other, err := repo.GetAllowedModels("key-2")
	if err != nil {
		t.Fatalf("GetAllowedModels key-2: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("one key's allowlist must not leak to another, got %v", other)
	}
}

func TestModelAccess_GetAllowedModelsForKeys(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, r *Repo)
		ids   []string
		want  map[string][]string
	}{
		{
			name:  "no ids",
			setup: func(*testing.T, *Repo) {},
			ids:   nil,
			want:  map[string][]string{},
		},
		{
			name: "every requested key is present",
			setup: func(t *testing.T, r *Repo) {
				if err := r.SetAllowedModels("key-1", []string{"b", "a"}); err != nil {
					t.Fatalf("set key-1: %v", err)
				}
			},
			ids: []string{"key-1", "key-2"},
			want: map[string][]string{
				"key-1": {"a", "b"},
				"key-2": {},
			},
		},
		{
			name:  "duplicate ids collapse",
			setup: func(*testing.T, *Repo) {},
			ids:   []string{"key-1", "key-1"},
			want:  map[string][]string{"key-1": {}},
		},
		{
			name:  "blank ids ignored",
			setup: func(*testing.T, *Repo) {},
			ids:   []string{"", "key-1"},
			want:  map[string][]string{"key-1": {}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := openEphemeralSchemaDB(t)
			tt.setup(t, repo)

			got, err := repo.GetAllowedModelsForKeys(tt.ids)
			if err != nil {
				t.Fatalf("GetAllowedModelsForKeys: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("want %d keys, got %v", len(tt.want), got)
			}
			for id, wantModels := range tt.want {
				gotModels, ok := got[id]
				if !ok {
					t.Errorf("key %q missing from result", id)
					continue
				}
				if len(gotModels) != len(wantModels) {
					t.Errorf("key %q: want %v, got %v", id, wantModels, gotModels)
					continue
				}
				for i := range wantModels {
					if gotModels[i] != wantModels[i] {
						t.Errorf("key %q position %d: want %q, got %q", id, i, wantModels[i], gotModels[i])
					}
				}
			}
		})
	}
}

func TestModelAccess_AgreesWithSingleKeyRead(t *testing.T) {
	repo := openEphemeralSchemaDB(t)

	if err := repo.SetAllowedModels("key-1", []string{"a", "b"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	// The batch read exists to avoid an N+1 when listing; it must return
	// exactly what the per-key read returns.
	batch, err := repo.GetAllowedModelsForKeys([]string{"key-1"})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	single, err := repo.GetAllowedModels("key-1")
	if err != nil {
		t.Fatalf("single: %v", err)
	}
	if len(batch["key-1"]) != len(single) {
		t.Fatalf("batch %v disagrees with single %v", batch["key-1"], single)
	}
	for i := range single {
		if batch["key-1"][i] != single[i] {
			t.Errorf("position %d: batch %q, single %q", i, batch["key-1"][i], single[i])
		}
	}
}

func TestModelAccess_NilRepoAndEmptyIDDegrade(t *testing.T) {
	var nilRepo *Repo

	got, err := nilRepo.GetAllowedModels("key-1")
	if err != nil {
		t.Errorf("nil repo must not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nil repo must allow all, got %v", got)
	}

	repo := openEphemeralSchemaDB(t)
	if err := repo.SetAllowedModels("", []string{"a"}); err == nil {
		t.Error("an empty apiKey id must be rejected, not silently stored")
	}
}

func TestModelAccess_TableExistsOnFreshSchema(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	if !repo.HasAllowedModelsTable() {
		t.Fatal("EnsureCoreSchema must create api_key_model_access")
	}
	// Guard the fixture's own dependency: the test DB path goes through
	// dbtest/direct schema helpers in some packages, so pin the file-based one.
	if _, err := filepath.Abs(t.TempDir()); err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	_ = fmt.Sprint()
}