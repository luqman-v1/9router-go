package executor

import (
	"fmt"
	"testing"
)

// #1167 item 1: the compiled-schema cache is keyed by client-supplied schema
// bytes and must stay bounded. Feeding it more distinct schemas than the cap
// leaves exactly the cap behind, and the schemas that survive still validate.
func TestDeepSeekSchemaCacheBounded(t *testing.T) {
	deepSeekSchemaCacheMu.Lock()
	saved := deepSeekSchemaCache
	deepSeekSchemaCache = map[string]deepSeekSchemaCacheEntry{}
	deepSeekSchemaCacheMu.Unlock()
	defer func() {
		deepSeekSchemaCacheMu.Lock()
		deepSeekSchemaCache = saved
		deepSeekSchemaCacheMu.Unlock()
	}()

	const extra = 25
	for i := 0; i < deepSeekSchemaCacheMaxEntries+extra; i++ {
		schema := fmt.Sprintf(`{"type":"object","properties":{"p%d":{"type":"string"}}}`, i)
		if _, ok := deepSeekCompiledSchema(schema); !ok {
			t.Fatalf("schema %d should compile", i)
		}
	}

	deepSeekSchemaCacheMu.RLock()
	size := len(deepSeekSchemaCache)
	deepSeekSchemaCacheMu.RUnlock()
	if size != deepSeekSchemaCacheMaxEntries {
		t.Fatalf("cache holds %d entries, want the cap %d", size, deepSeekSchemaCacheMaxEntries)
	}

	// The most recent schema survived eviction and still validates.
	last := fmt.Sprintf(`{"type":"object","properties":{"p%d":{"type":"string"}}}`, deepSeekSchemaCacheMaxEntries+extra-1)
	validate, ok := deepSeekCompiledSchema(last)
	if !ok {
		t.Fatal("newest schema should still be cached as compiled")
	}
	if err := validate(map[string]any{fmt.Sprintf("p%d", deepSeekSchemaCacheMaxEntries+extra-1): "x"}); err != nil {
		t.Fatalf("cached validator rejects a conforming argument: %v", err)
	}

	// Compile failures are cached too, and count against the same cap.
	if _, ok := deepSeekCompiledSchema(`{"type":"nope"}`); ok {
		t.Fatal("an unreadable schema must report not-ok")
	}
	deepSeekSchemaCacheMu.RLock()
	size = len(deepSeekSchemaCache)
	deepSeekSchemaCacheMu.RUnlock()
	if size > deepSeekSchemaCacheMaxEntries {
		t.Fatalf("cache grew past the cap after a failed compile: %d", size)
	}
}
