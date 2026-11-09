package providers

// ModelRenames maps a retired model id onto the id that replaced it.
//
// The gateway keeps serving the old name — an operator's saved capacity
// adapter pool still names it — and resolves it to the new one. The mapping
// belongs to the provider layer: a model id is a provider's name, not a
// settings-layer concern.
var ModelRenames = map[string]string{
	"oc/mimo-v2.5-free": "oc/mimo-v2.6-flash-free",
}

// CanonicalModelName returns the id a model name resolves to after the
// provider's renames, or the name itself when it is not renamed.
func CanonicalModelName(name string) string {
	if canonical, ok := ModelRenames[name]; ok {
		return canonical
	}
	return name
}
