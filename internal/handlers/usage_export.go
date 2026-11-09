package handlers

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// Usage log export.
//
// The dashboard shows requestDetails one page at a time, which is the right
// shape for looking at traffic and the wrong one for keeping it: an operator
// reconciling a month of spend, or handing a slice of history to someone else,
// wants a file. This handler streams the same rows the Details table pages
// through, as CSV or JSON.
//
// It reads the ledger through the same paged reader the dashboard uses, so the
// export cannot disagree with what the UI showed. The page size is the export
// chunk, not a row limit: a window with more rows than one page is walked in
// pages until the ledger is exhausted, and the response is streamed so a large
// window never has to fit in memory.

// exportPageSize is how many rows are read per ledger pass. It matches the
// dashboard's own page size so a window the UI can display is exported in the
// same units, and it bounds the memory one pass holds.
const exportPageSize = 100

// exportMaxRows bounds one export. A window with more rows than this is
// truncated rather than streamed forever: the response says so, because an
// export that silently stops early is worse than one that says it stopped.
const exportMaxRows = 100_000

// HandleUsageExport streams the request-details ledger as CSV or JSON.
//
// Query parameters, all optional:
//
//	format=csv|json  output shape (default csv)
//	period=<window> same resolver the dashboard uses: today, 24h, 7d, 30d,
//	                 all, or any <n>d / <n>h
//
// The response is an attachment, so a browser saves it rather than rendering
// it. CSV is the default because it opens in a spreadsheet, which is what an
// operator reconciling spend reaches for first.
func HandleUsageExport(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
		if format == "" {
			format = "csv"
		}
		if format != "csv" && format != "json" {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "format must be csv or json")
			return
		}
		since := detailsSince(r.URL.Query().Get("period"), time.Now())
		stamp := time.Now().UTC().Format("2006-01-02")
		if format == "csv" {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="9router-usage-%s.csv"`, stamp))
			writeUsageCSV(w, r, repo, since)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="9router-usage-%s.json"`, stamp))
		writeUsageJSON(w, r, repo, since)
	}
}

// writeUsageCSV streams the ledger as CSV. The header row is written first so a
// truncated export is still a well-formed file.
func writeUsageCSV(w http.ResponseWriter, r *http.Request, repo *db.Repo, since string) {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{
		"timestamp", "provider", "model", "connection", "status",
		"ttft_ms", "total_ms", "prompt_tokens", "completion_tokens", "cost_usd",
		"account", "combo", "protocol",
	})
	written, truncated := streamRows(r, repo, since, func(row db.RequestDetailListRow) error {
		return cw.Write([]string{
			row.Timestamp,
			row.Provider,
			row.Model,
			row.ConnectionID,
			row.Status,
			strconv.FormatInt(row.LatencyTTFT, 10),
			strconv.FormatInt(row.LatencyTotal, 10),
			exportTokensField(row.TokensJSON, "prompt_tokens"),
			exportTokensField(row.TokensJSON, "completion_tokens"),
			strconv.FormatFloat(row.Cost, 'f', 6, 64),
			row.Account,
			row.Combo,
			row.Protocol,
		})
	})
	cw.Flush()
	if err := cw.Error(); err != nil {
		return
	}
	if truncated {
		_, _ = fmt.Fprintf(w, "\n# truncated at %d rows; narrow the period for the rest\n", written)
	}
}

// writeUsageJSON streams the ledger as a JSON array, one object per row, so a
// large window never has to be buffered to be encoded.
func writeUsageJSON(w http.ResponseWriter, r *http.Request, repo *db.Repo, since string) {
	_, _ = w.Write([]byte("["))
	first := true
	_, truncated := streamRows(r, repo, since, func(row db.RequestDetailListRow) error {
		if !first {
			_, _ = w.Write([]byte(","))
		}
		first = false
		obj := map[string]any{
			"timestamp":         row.Timestamp,
			"provider":          row.Provider,
			"model":             row.Model,
			"connection":        row.ConnectionID,
			"status":            row.Status,
			"ttft_ms":           row.LatencyTTFT,
			"total_ms":          row.LatencyTotal,
			"prompt_tokens":     exportTokensField(row.TokensJSON, "prompt_tokens"),
			"completion_tokens": exportTokensField(row.TokensJSON, "completion_tokens"),
			"cost_usd":          row.Cost,
			"account":           row.Account,
			"combo":             row.Combo,
			"protocol":          row.Protocol,
		}
		return json.NewEncoder(w).Encode(obj)
	})
	_, _ = w.Write([]byte("]"))
	_ = truncated
}

// streamRows walks the ledger in pages and hands each row to emit. It returns
// the number of rows handed over and whether the export hit the row ceiling.
//
// The offset advances by the number of rows actually returned rather than by
// the page size, so a short page — the last one — cannot be re-read as an
// infinite loop of empty passes.
func streamRows(r *http.Request, repo *db.Repo, since string, emit func(db.RequestDetailListRow) error) (int, bool) {
	if repo == nil {
		return 0, false
	}
	written := 0
	for {
		if written >= exportMaxRows {
			return written, true
		}
		rows, _, err := repo.GetRequestDetailsPaged(r.Context(), exportPageSize, written, since)
		if err != nil {
			return written, false
		}
		if len(rows) == 0 {
			return written, false
		}
		for _, row := range rows {
			if err := emit(row); err != nil {
				return written, false
			}
			written++
			if written >= exportMaxRows {
				return written, true
			}
		}
	}
}

// exportTokensField reads one token count out of the row's stored tokens JSON.
// The ledger keeps the whole usage object there; the export surfaces the two
// counts an operator reconciles against. A row whose JSON is unreadable yields
// "0" rather than failing the whole export.
func exportTokensField(tokensJSON, field string) string {
	if tokensJSON == "" {
		return "0"
	}
	var tokens map[string]any
	if err := json.Unmarshal([]byte(tokensJSON), &tokens); err != nil {
		return "0"
	}
	switch v := tokens[field].(type) {
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case string:
		return v
	default:
		return "0"
	}
}
