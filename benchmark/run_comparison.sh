#!/bin/bash
# Benchmark comparison: Go Proxy vs Legacy Next.js 9Router
# Both use same mock upstream + same SQLite DB for fair comparison.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GO_PROXY_DIR="$(dirname "$SCRIPT_DIR")"
LEGACY_DIR="$(dirname "$GO_PROXY_DIR")"

MOCK_PORT=20199
GO_PORT=20131
LEGACY_PORT=20132
API_KEY="sk-benchmark-test-key"

CONCURRENCY_LEVELS=(1 5 10 25 50 100)
REQUESTS_PER_LEVEL=200

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

MOCK_PID=""
GO_PID=""
LEGACY_PID=""
BENCH_DB_DIR=""

cleanup() {
    echo -e "\n${YELLOW}Cleaning up...${NC}"
    [ -n "$MOCK_PID" ] && kill $MOCK_PID 2>/dev/null || true
    [ -n "$GO_PID" ] && kill $GO_PID 2>/dev/null || true
    [ -n "$LEGACY_PID" ] && kill $LEGACY_PID 2>/dev/null || true
    wait 2>/dev/null || true
    [ -n "$BENCH_DB_DIR" ] && rm -rf "$BENCH_DB_DIR" || true
}
trap cleanup EXIT

# Check hey
if ! command -v hey &> /dev/null; then
    echo -e "${RED}Installing 'hey'...${NC}"
    go install github.com/rakyll/hey@latest
    export PATH="$PATH:$(go env GOPATH)/bin"
fi

echo -e "${BLUE}╔══════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║    9Router Benchmark: Go Proxy vs Legacy Next.js        ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════════════════════╝${NC}"
echo ""

# Step 1: Mock upstream
echo -e "${GREEN}[1/5] Starting mock upstream on :$MOCK_PORT...${NC}"
cd "$GO_PROXY_DIR"
MOCK_PORT=$MOCK_PORT go run "$GO_PROXY_DIR/benchmark/mock_upstream.go" &
MOCK_PID=$!
sleep 2

# Retry check (Go compile takes a moment)
for i in 1 2 3 4 5; do
    if curl -s "http://127.0.0.1:$MOCK_PORT/v1/chat/completions" -H "Content-Type: application/json" -d '{"model":"m","messages":[{"role":"user","content":"x"}]}' > /dev/null 2>&1; then
        break
    fi
    sleep 2
done

if ! curl -s "http://127.0.0.1:$MOCK_PORT/v1/chat/completions" -H "Content-Type: application/json" -d '{"model":"m","messages":[{"role":"user","content":"x"}]}' > /dev/null 2>&1; then
    echo -e "${RED}Mock upstream failed${NC}"; exit 1
fi
echo "  Mock upstream ready."

# Step 2: Create temp DB
echo -e "${GREEN}[2/5] Setting up benchmark database...${NC}"
BENCH_DB_DIR=$(mktemp -d)
BENCH_DB="$BENCH_DB_DIR/db/data.sqlite"
mkdir -p "$BENCH_DB_DIR/db"

sqlite3 "$BENCH_DB" <<'SQL'
CREATE TABLE IF NOT EXISTS apiKeys (
    id TEXT PRIMARY KEY, key TEXT, name TEXT, machineId TEXT, isActive INTEGER DEFAULT 1, createdAt TEXT
);
INSERT INTO apiKeys (id, key, name, isActive, createdAt) VALUES
    ('bench-key', 'sk-benchmark-test-key', 'benchmark', 1, datetime('now'));

CREATE TABLE IF NOT EXISTS providerConnections (
    id TEXT PRIMARY KEY, provider TEXT, authType TEXT, name TEXT, email TEXT,
    priority INTEGER, isActive INTEGER DEFAULT 1, data TEXT, createdAt TEXT, updatedAt TEXT
);
INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
    ('bench-conn', 'openai-compatible-chat-bench', 'apikey', 'mock', 1, 1,
     json('{"apiKey":"sk-mock-key","providerSpecificData":{"baseUrl":"http://127.0.0.1:20199/v1"}}'), datetime('now'), datetime('now'));

CREATE TABLE IF NOT EXISTS providerNodes (
    id TEXT PRIMARY KEY, type TEXT, name TEXT, data TEXT, createdAt TEXT, updatedAt TEXT
);
INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
    ('openai-compatible-chat-bench', 'openai-compatible', 'mock-provider',
     json('{"prefix":"mock","apiType":"chat","baseUrl":"http://127.0.0.1:20199"}'),
     datetime('now'), datetime('now'));

CREATE TABLE IF NOT EXISTS combos (
    id TEXT PRIMARY KEY, name TEXT, kind TEXT, models TEXT, createdAt TEXT, updatedAt TEXT
);
INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
    ('bench-combo', 'bench-combo', 'fallback',
     '["mock/mock-model"]', datetime('now'), datetime('now'));

CREATE TABLE IF NOT EXISTS kv (scope TEXT, key TEXT, value TEXT, PRIMARY KEY(scope, key));
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS modelAliases (alias TEXT PRIMARY KEY, target TEXT);
SQL
echo "  Benchmark DB ready."

# Step 3: Build & start Go proxy
echo -e "${GREEN}[3/5] Building Go proxy...${NC}"
cd "$GO_PROXY_DIR"
go build -o /tmp/9router-bench-go "$GO_PROXY_DI./cmd/9router-go/"
DATA_DIR="$BENCH_DB_DIR" PORT=$GO_PORT /tmp/9router-bench-go &
GO_PID=$!
sleep 1

if ! curl -s "http://127.0.0.1:$GO_PORT/health" > /dev/null 2>&1; then
    echo -e "${RED}Go proxy failed${NC}"; exit 1
fi
echo -e "  Go proxy ready ${CYAN}(:$GO_PORT)${NC}"

# Step 4: Start legacy Next.js
echo -e "${GREEN}[4/5] Starting legacy Next.js...${NC}"
cd "$LEGACY_DIR"
DATA_DIR="$BENCH_DB_DIR" PORT=$LEGACY_PORT NODE_ENV=production node .next/standalone/server.js &
LEGACY_PID=$!
sleep 5

LEGACY_AVAILABLE=false
if curl -s "http://127.0.0.1:$LEGACY_PORT/api/v1/models" -H "Authorization: Bearer $API_KEY" > /dev/null 2>&1; then
    LEGACY_AVAILABLE=true
    echo -e "  Legacy Next.js ready ${CYAN}(:$LEGACY_PORT)${NC}"
else
    # Try alternate health check
    if curl -s "http://127.0.0.1:$LEGACY_PORT/" > /dev/null 2>&1; then
        LEGACY_AVAILABLE=true
        echo -e "  Legacy Next.js ready ${CYAN}(:$LEGACY_PORT)${NC} (alt check)"
    else
        echo -e "${YELLOW}  Legacy Next.js not responding, skipping comparison.${NC}"
    fi
fi

# Step 5: Benchmarks
echo -e "${GREEN}[5/5] Running benchmarks...${NC}"
echo ""

PAYLOAD='{"model":"mock/mock-model","messages":[{"role":"user","content":"Hello"}],"stream":false,"max_tokens":10}'
STREAM_PAYLOAD='{"model":"mock/mock-model","messages":[{"role":"user","content":"Hello"}],"stream":true,"max_tokens":10}'

run_bench() {
    local url=$1
    local payload=$2
    local c=$3
    local n=$4
    hey -n "$n" -c "$c" -m POST \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -d "$payload" \
        "$url" 2>/dev/null
}

parse_rps() { echo "$1" | grep "Requests/sec:" | awk '{print $2}'; }
parse_avg() { echo "$1" | grep "Average:" | head -1 | awk '{print $2}'; }
parse_p99() { echo "$1" | grep "99% in" | awk '{print $3}'; }

# ---- Non-Streaming ----
echo -e "${BLUE}━━━ Non-Streaming Chat Completions ━━━${NC}"
echo ""
printf "%-6s │ %-10s %-10s %-10s │ %-10s %-10s %-10s │ %s\n" \
    "c" "Go RPS" "Go Avg" "Go P99" "JS RPS" "JS Avg" "JS P99" "Speedup"
printf "%-6s─┼─%-10s─%-10s─%-10s─┼─%-10s─%-10s─%-10s─┼─%s\n" \
    "------" "----------" "----------" "----------" "----------" "----------" "----------" "-------"

for c in "${CONCURRENCY_LEVELS[@]}"; do
    n=$((c * 4)); [ $n -lt $REQUESTS_PER_LEVEL ] && n=$REQUESTS_PER_LEVEL

    GO_OUT=$(run_bench "http://127.0.0.1:$GO_PORT/v1/chat/completions" "$PAYLOAD" "$c" "$n")
    GO_RPS=$(parse_rps "$GO_OUT")
    GO_AVG=$(parse_avg "$GO_OUT")
    GO_P99=$(parse_p99 "$GO_OUT")

    if [ "$LEGACY_AVAILABLE" = true ]; then
        JS_OUT=$(run_bench "http://127.0.0.1:$LEGACY_PORT/v1/chat/completions" "$PAYLOAD" "$c" "$n")
        JS_RPS=$(parse_rps "$JS_OUT")
        JS_AVG=$(parse_avg "$JS_OUT")
        JS_P99=$(parse_p99 "$JS_OUT")

        SPEEDUP=$(echo "scale=1; $GO_RPS / $JS_RPS" | bc 2>/dev/null || echo "N/A")

        printf "%-6s │ %-10s %-10s %-10s │ %-10s %-10s %-10s │ %sx\n" \
            "c=$c" "$GO_RPS" "${GO_AVG}s" "${GO_P99}s" "$JS_RPS" "${JS_AVG}s" "${JS_P99}s" "$SPEEDUP"
    else
        printf "%-6s │ %-10s %-10s %-10s │ %-10s\n" \
            "c=$c" "$GO_RPS" "${GO_AVG}s" "${GO_P99}s" "(skip)"
    fi
done
echo ""

# ---- Streaming ----
echo -e "${BLUE}━━━ Streaming Chat Completions ━━━${NC}"
echo ""
printf "%-6s │ %-10s %-10s %-10s │ %-10s %-10s %-10s │ %s\n" \
    "c" "Go RPS" "Go Avg" "Go P99" "JS RPS" "JS Avg" "JS P99" "Speedup"
printf "%-6s─┼─%-10s─%-10s─%-10s─┼─%-10s─%-10s─%-10s─┼─%s\n" \
    "------" "----------" "----------" "----------" "----------" "----------" "----------" "-------"

for c in "${CONCURRENCY_LEVELS[@]}"; do
    n=$((c * 4)); [ $n -lt $REQUESTS_PER_LEVEL ] && n=$REQUESTS_PER_LEVEL

    GO_OUT=$(run_bench "http://127.0.0.1:$GO_PORT/v1/chat/completions" "$STREAM_PAYLOAD" "$c" "$n")
    GO_RPS=$(parse_rps "$GO_OUT")
    GO_AVG=$(parse_avg "$GO_OUT")
    GO_P99=$(parse_p99 "$GO_OUT")

    if [ "$LEGACY_AVAILABLE" = true ]; then
        JS_OUT=$(run_bench "http://127.0.0.1:$LEGACY_PORT/v1/chat/completions" "$STREAM_PAYLOAD" "$c" "$n")
        JS_RPS=$(parse_rps "$JS_OUT")
        JS_AVG=$(parse_avg "$JS_OUT")
        JS_P99=$(parse_p99 "$JS_OUT")

        SPEEDUP=$(echo "scale=1; $GO_RPS / $JS_RPS" | bc 2>/dev/null || echo "N/A")

        printf "%-6s │ %-10s %-10s %-10s │ %-10s %-10s %-10s │ %sx\n" \
            "c=$c" "$GO_RPS" "${GO_AVG}s" "${GO_P99}s" "$JS_RPS" "${JS_AVG}s" "${JS_P99}s" "$SPEEDUP"
    else
        printf "%-6s │ %-10s %-10s %-10s │ %-10s\n" \
            "c=$c" "$GO_RPS" "${GO_AVG}s" "${GO_P99}s" "(skip)"
    fi
done
echo ""

# ---- Memory ----
echo -e "${BLUE}━━━ Memory Usage ━━━${NC}"
GO_MEM=$(ps -o rss= -p $GO_PID 2>/dev/null | awk '{printf "%.1f MB", $1/1024}')
echo -e "  Go Proxy:    ${GREEN}$GO_MEM${NC}"
if [ "$LEGACY_AVAILABLE" = true ]; then
    JS_MEM=$(ps -o rss= -p $LEGACY_PID 2>/dev/null | awk '{printf "%.1f MB", $1/1024}')
    echo -e "  Next.js:     ${RED}$JS_MEM${NC}"
    MEM_RATIO=$(echo "scale=1; $(ps -o rss= -p $LEGACY_PID 2>/dev/null) / $(ps -o rss= -p $GO_PID 2>/dev/null)" | bc 2>/dev/null || echo "N/A")
    echo -e "  Ratio:       ${CYAN}${MEM_RATIO}x${NC} (Next.js / Go)"
fi
echo ""
echo -e "${GREEN}Benchmark complete.${NC}"
