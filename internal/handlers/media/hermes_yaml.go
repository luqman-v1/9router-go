package media

import (
	"fmt"
	"regexp"
	"strings"
)

// Hermes `config.yaml` / `.env` editor. Port of upstream
// `src/app/api/cli-tools/hermes-settings/hermesYaml.js`.
//
// The edits are deliberately regex-based rather than a full YAML round-trip:
// unmarshalling into a map and re-marshalling would drop every comment,
// key order and formatting choice Hermes put in the file, and would happily
// delete user-owned keys that are not part of the three blocks we manage.
// Surgically replacing only our own blocks is the only safe way to write into
// a file a human also edits.
//
// Shapes (https://hermes-agent.nousresearch.com/docs/user-guide/configuration):
//
//	model:                 # top-level mapping block (indented children)
//	  provider: "custom"
//	  default: "provider/model"
//	model: ""              # fresh install ships this scalar sentinel ("not configured")
//	delegation: { ... }    # top-level, subagent model
//	auxiliary: { <task>: { provider, model, base_url, api_key } }

var (
	// modelBlockRE matches the top-level "model:" mapping block, key on its
	// own line with indented children. It deliberately does NOT match the
	// scalar sentinel `model: ""` (no newline right after the colon).
	modelBlockRE = regexp.MustCompile(`(?m)^model:[ \t]*\r?\n((?:[ \t]+.*\r?\n?|[ \t]*\r?\n)*)`)
	// modelScalarRE matches the single-line form: `model: ""`, `model: ''`,
	// `model:` at EOF. Anchored without leading indent and with `:` required
	// to follow "model", so `model_aliases:` never matches.
	modelScalarRE = regexp.MustCompile(`(?m)^model:[ \t]*[^\r\n]*\r?\n?`)
	// delegationBlockRE matches the top-level "delegation:" block, up to the
	// next non-indented non-empty line.
	delegationBlockRE = regexp.MustCompile(`(?m)^delegation:[ \t]*\r?\n((?:[ \t]+.*\r?\n?|[ \t]*\r?\n)*)`)
	// auxBlockRE matches "auxiliary:" and its body: 2-space-indented role
	// keys with 4+-space fields.
	auxBlockRE = regexp.MustCompile(`(?m)^auxiliary:[ \t]*\r?\n((?:(?:[ \t]+.*\r?\n?)|(?:[ \t]*\r?\n))*)`)
	// auxRoleSubRE finds one role key inside the auxiliary body.
	auxRoleSubRE = regexp.MustCompile(`(?m)^  ([A-Za-z0-9_]+):[ \t]*\r?\n((?:(?:[ \t]{4,}.*\r?\n?)|(?:[ \t]*\r?\n))*)`)
	// displayNameRE reads `display_name` out of profile.yaml. Presentation
	// only — the canonical profile id stays the directory name.
	displayNameRE = regexp.MustCompile(`(?m)^display_name:[ \t]*["']?([^"'\r\n]+)["']?`)
)

// apiKeyEnvPlaceholder is written verbatim into config.yaml: Hermes expands
// ${OPENAI_API_KEY} from the profile's .env at load time, so the secret never
// lands in the YAML.
const apiKeyEnvPlaceholder = "${OPENAI_API_KEY}"

// hermesModelBlock is the parsed top-level "model:" mapping.
type hermesModelBlock struct {
	Default  *string `json:"default"`
	Provider *string `json:"provider"`
	BaseURL  *string `json:"base_url"`
	APIKey   *string `json:"api_key"`
}

// The block accessors below are nil-safe: a missing block and a block missing
// one field must read the same as an absent value, because the caller asks
// "is this ours?" and an unparsed block is not.
func (b *hermesModelBlock) defaultModel() *string {
	if b == nil {
		return nil
	}
	return b.Default
}

func (b *hermesModelBlock) provider() *string {
	if b == nil {
		return nil
	}
	return b.Provider
}

func (b *hermesModelBlock) baseURL() *string {
	if b == nil {
		return nil
	}
	return b.BaseURL
}

func (b *hermesDelegationBlock) provider() *string {
	if b == nil {
		return nil
	}
	return b.Provider
}

func (b *hermesDelegationBlock) baseURL() *string {
	if b == nil {
		return nil
	}
	return b.BaseURL
}

// hermesDelegationBlock is the parsed "delegation:" block.
type hermesDelegationBlock struct {
	Model    *string `json:"model"`
	BaseURL  *string `json:"base_url"`
	Provider *string `json:"provider"`
}

// hermesAuxRole is one role under "auxiliary:".
type hermesAuxRole struct {
	Model    *string `json:"model"`
	BaseURL  *string `json:"base_url"`
	Provider *string `json:"provider"`
}

func buildModelBlock(model, baseURL string) string {
	return fmt.Sprintf("model:\n  default: %q\n  provider: \"custom\"\n  base_url: %q\n  api_key: %s\n",
		model, baseURL, apiKeyEnvPlaceholder)
}

func buildDelegationBlock(model, baseURL string) string {
	return fmt.Sprintf("delegation:\n  model: %q\n  provider: \"custom\"\n  base_url: %q\n  api_key: %s\n",
		model, baseURL, apiKeyEnvPlaceholder)
}

func buildAuxRoleBlock(role, model, baseURL string) string {
	return fmt.Sprintf("  %s:\n    provider: \"custom\"\n    model: %q\n    base_url: %q\n    api_key: %s\n",
		role, model, baseURL, apiKeyEnvPlaceholder)
}

// yamlField pulls an indented `key: value` out of a block body. Quotes are
// stripped and the value trimmed, matching the reference's best-effort parse.
func yamlField(body, key string) *string {
	re := regexp.MustCompile(`(?m)^[ \t]+` + regexp.QuoteMeta(key) + `:[ \t]*["']?([^"'\r\n]+)["']?`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	return new(strings.TrimSpace(m[1]))
}

// parseHermesModelBlock returns the current model block, or nil when there is
// no mapping block — unset, or the `model: ""` sentinel.
func parseHermesModelBlock(yaml string) *hermesModelBlock {
	m := modelBlockRE.FindStringSubmatch(yaml)
	if m == nil {
		return nil
	}
	body := m[1]
	return &hermesModelBlock{
		Default:  yamlField(body, "default"),
		Provider: yamlField(body, "provider"),
		BaseURL:  yamlField(body, "base_url"),
		APIKey:   yamlField(body, "api_key"),
	}
}

// parseHermesDelegationBlock returns the delegation block, or nil when absent.
func parseHermesDelegationBlock(yaml string) *hermesDelegationBlock {
	m := delegationBlockRE.FindStringSubmatch(yaml)
	if m == nil {
		return nil
	}
	body := m[1]
	return &hermesDelegationBlock{
		Model:    yamlField(body, "model"),
		BaseURL:  yamlField(body, "base_url"),
		Provider: yamlField(body, "provider"),
	}
}

// parseHermesAuxRoles returns every role under "auxiliary:", keyed by role id.
func parseHermesAuxRoles(yaml string) map[string]hermesAuxRole {
	roles := map[string]hermesAuxRole{}
	m := auxBlockRE.FindStringSubmatch(yaml)
	if m == nil {
		return roles
	}
	for _, sm := range auxRoleSubRE.FindAllStringSubmatch(m[1], -1) {
		body := sm[2]
		roles[sm[1]] = hermesAuxRole{
			Model:    yamlField(body, "model"),
			BaseURL:  yamlField(body, "base_url"),
			Provider: yamlField(body, "provider"),
		}
	}
	return roles
}

// upsertModelBlock replaces the model block, or appends it. The scalar sentinel
// is replaced too: leaving `model: ""` behind would produce a duplicate
// `model:` key, and most YAML loaders take the last one — silently ignoring
// the block we just wrote.
func upsertModelBlock(yaml, block string) string {
	if modelBlockRE.MatchString(yaml) {
		return modelBlockRE.ReplaceAllString(yaml, block)
	}
	if modelScalarRE.MatchString(yaml) {
		return modelScalarRE.ReplaceAllString(yaml, block)
	}
	if yaml == "" {
		return block
	}
	return block + "\n" + yaml
}

func upsertDelegationBlock(yaml, block string) string {
	if delegationBlockRE.MatchString(yaml) {
		return delegationBlockRE.ReplaceAllString(yaml, block)
	}
	if yaml == "" || strings.HasSuffix(yaml, "\n") {
		return yaml + block
	}
	return yaml + "\n" + block
}

func removeDelegationBlock(yaml string) string {
	return delegationBlockRE.ReplaceAllString(yaml, "")
}

// auxRoleRE builds the matcher for one role key inside the auxiliary body.
func auxRoleRE(role string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(role) + `:[ \t]*\r?\n(?:(?:[ \t]{4,}.*\r?\n?)|(?:[ \t]*\r?\n))*`)
}

// upsertHermesAuxRole replaces one role block under "auxiliary:", creating the
// whole section when it is missing.
func upsertHermesAuxRole(yaml, role, roleBlock string) string {
	re := auxRoleRE(role)
	m := auxBlockRE.FindStringSubmatch(yaml)
	if m == nil {
		block := "auxiliary:\n" + roleBlock
		if yaml == "" || strings.HasSuffix(yaml, "\n") {
			return yaml + block
		}
		return yaml + "\n" + block
	}
	body := m[1]
	if re.MatchString(body) {
		body = re.ReplaceAllString(body, roleBlock)
	} else {
		body += roleBlock
	}
	return auxBlockRE.ReplaceAllString(yaml, "auxiliary:\n"+body)
}

// removeHermesAuxRole drops one role from "auxiliary:", and drops the whole
// section when it was the last role.
func removeHermesAuxRole(yaml, role string) string {
	m := auxBlockRE.FindStringSubmatch(yaml)
	if m == nil {
		return yaml
	}
	body := auxRoleRE(role).ReplaceAllString(m[1], "")
	if strings.TrimSpace(body) == "" {
		return auxBlockRE.ReplaceAllString(yaml, "")
	}
	return auxBlockRE.ReplaceAllString(yaml, "auxiliary:\n"+body)
}

// removeModelBlock removes either the mapping block or the scalar sentinel.
func removeModelBlock(yaml string) string {
	out := yaml
	if modelBlockRE.MatchString(out) {
		out = modelBlockRE.ReplaceAllString(out, "")
	} else if modelScalarRE.MatchString(out) {
		out = modelScalarRE.ReplaceAllString(out, "")
	}
	return strings.TrimLeft(out, "\n")
}

// upsertEnvVar sets a single KEY=VALUE line, replacing an existing one.
func upsertEnvVar(envText, key, value string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `=.*$`)
	line := key + "=" + value
	if re.MatchString(envText) {
		return re.ReplaceAllString(envText, line)
	}
	if envText != "" && !strings.HasSuffix(envText, "\n") {
		return envText + "\n" + line + "\n"
	}
	return envText + line + "\n"
}

// removeEnvVar deletes a single KEY=VALUE line.
func removeEnvVar(envText, key string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `=.*\r?\n?`)
	return re.ReplaceAllString(envText, "")
}

// hermesLocalEndpointRE matches the loopback hosts 9router itself binds.
var hermesLocalEndpointRE = regexp.MustCompile(`localhost|127\.0\.0\.1|0\.0\.0\.0`)

// has9RouterConfig reports a block pointing at a local 9router endpoint.
// Detection only: a tunnel endpoint reads false here and surfaces as "other"
// in the UI, exactly like it does upstream.
func has9RouterConfig(provider, baseURL *string) bool {
	if baseURL == nil || *baseURL == "" {
		return false
	}
	return provider != nil && *provider == "custom" && hermesLocalEndpointRE.MatchString(*baseURL)
}

// isCustomBlock reports a block 9router wrote (provider "custom") regardless of
// endpoint — covers tunnel URLs, so reset and bulk-apply can still find them.
func isCustomBlock(provider *string) bool {
	return provider != nil && *provider == "custom"
}

// parseHermesDisplayName reads display_name out of a profile.yaml body.
func parseHermesDisplayName(yaml string) *string {
	m := displayNameRE.FindStringSubmatch(yaml)
	if m == nil {
		return nil
	}
	return new(strings.TrimSpace(m[1]))
}
