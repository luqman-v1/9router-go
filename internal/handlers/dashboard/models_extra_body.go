package dashboard

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	"9router/proxy/internal/db"
)

// validateCustomModelExtraBody checks the optional extraBody of a single
// custom-model save. The row is data["value"] (object or JSON string) or data
// itself. A null or empty extraBody clears the field: it is removed from the
// row so the stored value never carries a placeholder. A valid extraBody is
// re-pointed at its original request bytes, because data went through a
// float64 decode that would round an integer such as a 17-digit seed.
func validateCustomModelExtraBody(body []byte, data map[string]any, key string) error {
	row := customModelRow(data)
	if row == nil {
		return nil
	}
	v, present := row["extraBody"]
	if !present {
		return nil
	}
	if v == nil || v == "" {
		delete(row, "extraBody")
		return nil
	}
	if !db.ExtraBodyProviderAllowed(customModelProvider(row, key)) {
		return db.ErrExtraBodyProvider
	}
	raw := rawExtraBody(body)
	if _, err := db.ParseExtraBody(raw); err != nil {
		return err
	}
	row["extraBody"] = raw
	return nil
}

// customModelProvider is the alias the row is stored under: its own
// providerAlias, else the key's first "alias|id|type" segment.
func customModelProvider(row map[string]any, key string) string {
	if p, ok := row["providerAlias"].(string); ok && p != "" {
		return p
	}
	if alias, _, found := strings.Cut(key, "|"); found {
		return alias
	}
	return ""
}

// rawExtraBody returns the extraBody bytes exactly as the client sent them,
// from value (object or JSON string) or from the top level.
func rawExtraBody(body []byte) jsontext.Value {
	var envelope struct {
		ExtraBody jsontext.Value `json:"extraBody"`
		Value     jsontext.Value `json:"value"`
	}
	_ = json.Unmarshal(body, &envelope) // body already decoded once by the caller
	inner := envelope.Value
	if inner.Kind() == '"' {
		var s string
		if json.Unmarshal(inner, &s) == nil {
			inner = jsontext.Value(s)
		}
	}
	if inner.Kind() == '{' {
		var row struct {
			ExtraBody jsontext.Value `json:"extraBody"`
		}
		if json.Unmarshal(inner, &row) == nil {
			return row.ExtraBody
		}
	}
	return envelope.ExtraBody
}

// customModelRow returns the object the save will store, re-pointing a string
// value at its parsed form so a cleared extraBody is dropped from it too. A
// string that is not a JSON object is stored verbatim, as before.
func customModelRow(data map[string]any) map[string]any {
	value, ok := data["value"]
	if !ok {
		return data
	}
	switch v := value.(type) {
	case map[string]any:
		return v
	case string:
		var row map[string]any
		if err := json.Unmarshal([]byte(v), &row); err != nil || row == nil {
			return nil
		}
		if _, has := row["extraBody"]; !has {
			return nil
		}
		data["value"] = row
		return row
	}
	return nil
}
