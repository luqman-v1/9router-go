package chat

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"

	"9router/proxy/internal/db"
	"9router/proxy/internal/dbtest"
	"9router/proxy/internal/models"
)

// settingsRowReads counts how many times one request paid for the settings row.
var settingsRowReads atomic.Int64

// countingDriver wraps the sqlite driver and counts reads of the settings row,
// so a test can assert what a request actually paid for.
type countingDriver struct{ inner *sqlite.Driver }

func (d *countingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn}, nil
}

type countingConn struct{ driver.Conn }

func countIfSettingsRow(query string) {
	if strings.Contains(query, "FROM settings WHERE id = 1") {
		settingsRowReads.Add(1)
	}
}

// QueryContext is the only entry database/sql uses for a row read, and it is
// the one the modernc driver implements, so the deprecated driver.Queryer
// fallback is deliberately absent.
func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	countIfSettingsRow(query)
	if q, ok := c.Conn.(driver.QueryerContext); ok {
		return q.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func init() { sql.Register("sqlite_settings_count", &countingDriver{inner: &sqlite.Driver{}}) }

// TestRequestPathHelpersShareOneSettingsRead proves the request path pays for
// the settings row a single time. Six helpers used to read it independently
// (deadline, thinking modes, proxy pool, model assignment, connection
// eligibility, usage detail), so the same row was fetched — point query plus
// clone — once per helper.
func TestRequestPathHelpersShareOneSettingsRead(t *testing.T) {
	database, cleanup := setupCountingChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	conn := &models.ProviderConnection{ID: "conn-1", Provider: "deepseek", IsActive: 1}

	ctx := withRequestSettingsHolder(context.Background())
	before := settingsRowReads.Load()
	// The six request-path settings readers of the chat handler.
	handler.totalRequestTimeout(ctx)
	handler.providerThinkingMode(ctx, "deepseek")
	handler.withProviderThinkingModes(ctx)
	handler.resolveProviderProxyPoolID(ctx, "deepseek", map[string]bool{})
	handler.pinnedConnectionIneligible(ctx, conn, nil, "deepseek-chat")
	handler.shouldLogRequestDetail(ctx, false)
	reads := settingsRowReads.Load() - before

	if reads != 1 {
		t.Fatalf("settings row read %d times across the request path, want exactly 1", reads)
	}
}

// TestSettingsHolderDoesNotOutliveTheRequest proves the memo is per request:
// the next request reads the row again, so a settings change the dashboard
// writes between two requests is never served stale.
func TestSettingsHolderDoesNotOutliveTheRequest(t *testing.T) {
	database, cleanup := setupCountingChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	first := withRequestSettingsHolder(context.Background())
	before := settingsRowReads.Load()
	handler.totalRequestTimeout(first)
	if reads := settingsRowReads.Load() - before; reads != 1 {
		t.Fatalf("first request read the row %d times, want 1", reads)
	}
	// The very next request, holding its own holder: another read, not a stale
	// memo from the request that just finished.
	second := withRequestSettingsHolder(context.Background())
	before = settingsRowReads.Load()
	handler.totalRequestTimeout(second)
	if reads := settingsRowReads.Load() - before; reads != 1 {
		t.Fatalf("second request read the row %d times, want 1", reads)
	}
}

// TestSettingsForRequestSurvivesANilRepo pins the failure-closed shape several
// probe paths rely on: no repository means no settings, never a panic.
func TestSettingsForRequestSurvivesANilRepo(t *testing.T) {
	handler := &ChatHandler{}
	if settings := handler.settingsForRequest(context.Background()); settings != nil {
		t.Fatalf("a nil repo resolved to %+v, want nil", settings)
	}
	var typed *ChatHandler
	if settings := typed.settingsForRequest(context.Background()); settings != nil {
		t.Fatalf("a nil handler resolved to %+v, want nil", settings)
	}
}

// setupCountingChatTestDB builds the chat test schema on a database whose
// driver counts settings row reads.
func setupCountingChatTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	tmp := t.TempDir() + "/counting.sqlite"

	database, err := sql.Open("sqlite_settings_count", tmp)
	if err != nil {
		t.Fatalf("open counting sqlite: %v", err)
	}
	if err := dbtest.CreateTables(database); err != nil {
		database.Close()
		t.Fatalf("CreateTables: %v", err)
	}
	return database, func() { database.Close() }
}

// TestSettingsForRequestSurvivesAnUnreadableRow pins the failure shape the
// request path must keep: a settings row that cannot be read resolves to nil,
// never a panic. A request whose database is already unhappy must not take the
// process down with it.
func TestSettingsForRequestSurvivesAnUnreadableRow(t *testing.T) {
	database, err := sql.Open("sqlite_settings_count", t.TempDir()+"/empty.sqlite")
	if err != nil {
		t.Fatalf("open empty sqlite: %v", err)
	}
	defer database.Close()

	handler := NewChatHandler(db.NewRepo(database))
	ctx := withRequestSettingsHolder(context.Background())

	if settings := handler.settingsForRequest(ctx); settings != nil {
		t.Fatalf("an unreadable row resolved to %+v, want nil", settings)
	}
	if got := handler.totalRequestTimeout(ctx); got != 0 {
		t.Fatalf("totalRequestTimeout = %v with an unreadable row, want off", got)
	}
	if !handler.shouldLogRequestDetail(ctx, false) {
		t.Fatal("an unreadable row switched request-detail logging off")
	}
}
