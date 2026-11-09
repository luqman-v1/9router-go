package modelhealth

import (
	json "encoding/json/v2"
	"strings"

	"9router/proxy/internal/db"
)

// RepoStore adapts the dashboard repository to the loop's Store interface.
//
// It exists so the loop package does not import the db package: the loop is
// the part worth testing without a database, and the KV scope names stay in
// one place rather than being spelled at every call site.
type RepoStore struct {
	Repo *db.Repo
}

// GetKV reads one key of one scope, returning "" when absent.
func (s RepoStore) GetKV(scope, key string) (string, error) {
	all, err := s.Repo.GetKVScope(scope)
	if err != nil {
		return "", err
	}
	return all[key], nil
}

// SetKV writes one key of one scope.
func (s RepoStore) SetKV(scope, key, value string) error {
	return s.Repo.SetKV(scope, key, value)
}

// DisabledModels returns the current disabled list for one provider.
//
// The value is decoded leniently: a missing row is an empty list, and a
// malformed one is treated as empty rather than as an error, because the loop
// is about to overwrite it anyway and refusing to read it would stop the
// loop from working.
func (s RepoStore) DisabledModels(provider string) ([]string, error) {
	raw, err := s.GetKV("disabledModels", provider)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, nil
	}
	return ids, nil
}

// SaveDisabledModels writes the disabled list for one provider.
func (s RepoStore) SaveDisabledModels(provider string, ids []string) error {
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return s.Repo.SetKV("disabledModels", provider, string(encoded))
}
