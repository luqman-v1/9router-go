package db

import "testing"

func TestRewriteJSONSetQuery(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{
			"no json_set", "SELECT 1", "SELECT 1",
		},
		{
			"plain args",
			"UPDATE t SET data = json_set(data, $1, $2)",
			"UPDATE t SET data = pg_json_set(data, ($1)::text, ($2)::text)",
		},
		{
			"int literal and NULL",
			"json_set(data, '$.backoffLevel', 0, '$.status', NULL)",
			"pg_json_set(data, ('$.backoffLevel')::text, (0)::text, ('$.status')::text, (NULL)::text)",
		},
		{
			"json() arg",
			"json_set(data, '$.lastError', json($1))",
			"pg_json_set(data, ('$.lastError')::text, ($1::text)::text)",
		},
		{
			"json('null') arg",
			"json_set(data, $1, json('null'))",
			"pg_json_set(data, ($1)::text, ('null'::text)::text)",
		},
		{
			"multiple args",
			"UPDATE x SET data = json_set(data, ?, ?, '$.backoffLevel', ?)",
			"UPDATE x SET data = pg_json_set(data, (?)::text, (?)::text, ('$.backoffLevel')::text, (?)::text)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rewriteJSONSetQuery(tt.in); got != tt.want {
				t.Fatalf("rewriteJSONSetQuery() = %q, want %q", got, tt.want)
			}
		})
	}
}
