package db

import "testing"

func TestRebindQuery(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"no-op without ?", "SELECT 1", "SELECT 1"},
		{"single ?", "SELECT * FROM t WHERE id = ?", "SELECT * FROM t WHERE id = $1"},
		{"two ?", "INSERT INTO t(a,b) VALUES(?,?)", "INSERT INTO t(a,b) VALUES($1,$2)"},
		{
			"? inside string literal untouched",
			"SELECT * FROM t WHERE name = 'a?b' AND id = ?",
			"SELECT * FROM t WHERE name = 'a?b' AND id = $1",
		},
		{
			"? inside line comment untouched",
			"SELECT 1 -- comment ?\nFROM t WHERE id = ?",
			"SELECT 1 -- comment ?\nFROM t WHERE id = $1",
		},
		{
			"? inside dollar-quoted string untouched",
			"SELECT $$a?b$$, ?",
			"SELECT $$a?b$$, $1",
		},
		{
			"json_set path $ untouched, ? rewritten and shimmed",
			"json_set(data, ?, ?, '$.backoffLevel', ?)",
			"pg_json_set(data, ($1)::text, ($2)::text, ('$.backoffLevel')::text, ($3)::text)",
		},
		{
			"json_extract path $ untouched, ? rewritten",
			"json_extract(tokens, '$.cached_tokens') = ?",
			"json_extract(tokens, '$.cached_tokens') = $1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rebindQuery(tt.in); got != tt.want {
				t.Fatalf("rebindQuery(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
