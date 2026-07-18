#!/bin/bash
# Benchmark: Go proxy vs Legacy Next.js 9Router
# Uses a mock upstream server for fair, reproducible comparison.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GO_PROXY_DIR="$(dirname "$SCRIPT_DIR")"
LEGACY_DIR="$(dirname "$GO_PROXY_DIR")"

MOCK_PORT=20199
GO_PORT=20131
LEGACY_PORT=20132
RESULTS_FILE="$SCRIPT_DIR/results.md"

CONCURRENCY_LEVELS=(1 5 10 25 50 100)
REQUESTS_PER_LEVEL=200
API_KEY="sk-benchmark-test-key"

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

cleanup() {
    echo -e "${YELLOW}Cleaning up...${NC}"
    kill $MOCK_PID $GO_PID $LEGACY_PID 2>/dev/null || true
    wait $MOCK_PID $GO_PID $LEGACY_PID 2>/dev/null || true
}
trap cleanup EXIT

# Check if hey is installed
if ! command -v hey &> /dev/null; then
    echo -e "${RED}Installing 'hey' HTTP benchmarking tool...${NC}"
    if command -v go &> /dev/null; then
        go install github.com/rakyll/hey@latest
        export PATH="$PATH:$(go env GOPATH)/bin"
    else
        echo "Install 'hey': go install github.com/rakyll/hey@latest"
        exit 1
    fi
fi

echo -e "${BLUE}=== 9Router Benchmark: Go Proxy vs Legacy Next.js ===${NC}"
echo ""

# Step 1: Start mock upstream
echo -e "${GREEN}[1/5] Starting mock upstream on :$MOCK_PORT...${NC}"
go run "$SCRIPT_DIR/mock_upstream.go" &
MOCK_PID=$!
sleep 1

# Verify mock is up
if ! curl -s "http://127.0.0.1:$MOCK_PORT/v1/chat/completions" > /dev/null 2>&1; then
    echo -e "${RED}Mock upstream failed to start${NC}"
    exit 1
fi
echo "  Mock upstream ready."

# Step 2: Create temp SQLite DB for benchmark
echo -e "${GREEN}[2/5] Setting up benchmark database...${NC}"
BENCH_DB_DIR=$(mktemp -d)
BENCH_DB="$BENCH_DB_DIR/data.sqlite"

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
     json('{"apiKey":"sk-mock-key"}'), datetime('now'), datetime('now'));

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
echo "  Benchmark DB ready at $BENCH_DB"

# Step 3: Build and start Go proxy
echo -e "${GREEN}[3/5] Building and starting Go proxy on :$GO_PORT...${NC}"
cd "$GO_PROXY_DIR"
go build -o /tmp/9router-bench-proxy ./cmd/9router-proxy/
DATA_DIR="$BENCH_DB_DIR" PORT=$GO_PORT /tmp/9router-bench-proxy &
GO_PID=$!
sleep 2

if ! curl -s "http://127.0.0.1:$GO_PORT/health" > /dev/null 2>&1; then
    echo -e "${RED}Go proxy failed to start${NC}"
    exit 1
fi
echo "  Go proxy ready."

# Step 4: Start legacy Next.js proxy (if available)
LEGACY_AVAILABLE=false
if [ -f "$LEGACY_DIR/node_modules/.bin/next" ]; then
    echo -e "${GREEN}[4/5] Starting legacy Next.js proxy on :$LEGACY_PORT...${NC}"
    cd "$LEGACY_DIR"
    PORT=$LEGACY_PORT NODE_ENV=production npm run start > /dev/null 2>&1 &
    LEGACY_PID=$!
    sleep 5

    if curl -s "http://127.0.0.1:$LEGACY_PORT/api/settings" > /dev/null 2>&1; then
        LEGACY_AVAILABLE=true
        echo "  Legacy proxy ready."
    else
        echo -e "${YELLOW}  Legacy proxy not responding, skipping comparison.${NC}"
    fi
else
    echo -e "${YELLOW}[4/5] Legacy Next.js not built, skipping.${NC}"
fi

# Step 5: Run benchmarks
echo -e "${GREEN}[5/5] Running benchmarks...${NC}"
echo ""

# Payload
PAYLOAD='{"model":"mock/mock-model","messages":[{"role":"user","content":"Hello"}],"stream":false,"max_tokens":10}'
STREAM_PAYLOAD='{"model":"mock/mock-model","messages":[{"role":"user","content":"Hello"}],"stream":true,"max_tokens":10}'

# Results arrays
declare -A GO_RESULTS
declare -A LEGACY_RESULTS

benchmark_endpoint() {
    local name=$1
    local url=$2
    local payload=$3
    local concurrency=$4
    local requests=$5

    hey -n "$requests" -c "$concurrency" -m POST \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -d "$payload" \
        "$url" 2>/dev/null
}

parse_hey_output() {
    local output=$1
    local rps=$(echo "$output" | grep "Requests/sec:" | awk '{print $2}')
    local avg_latency=$(echo "$output" | grep "Average:" | head -1 | awk '{print $2}')
    local p50=$(echo "$output" | grep "50% in" | awk '{print $3}')
    local p99=$(echo "$output" | grep "99% in" | awk '{print $3}')
    echo "$rps|$avg_latency|$p50|$p99"
}

# Run non-streaming benchmark
echo -e "${BLUE}--- Non-Streaming Chat Completions ---${NC}"
echo ""

printf "%-12s | %-8s | %-20s | %-20s\n" "Concurrent" "RPS" "Avg Latency" "P99 Latency"
printf "%-12s-+-%-8s-+-%-20s-+-%-20s\n" "------------" "--------" "--------------------" "--------------------"

for c in "${CONCURRENCY_LEVELS[@]}"; do
    n=$((c * 4))
    if [ $n -lt $REQUESTS_PER_LEVEL ]; then
        n=$REQUESTS_PER_LEVEL
    fi

    GO_OUT=$(benchmark_endpoint "non-stream" "http://127.0.0.1:$GO_PORT/v1/chat/completions" "$PAYLOAD" "$c" "$n")
    GO_PARSED=$(parse_hey_output "$GO_OUT")
    GO_RPS=$(echo "$GO_PARSED" | cut -d'|' -f1)
    GO_AVG=$(echo "$GO_PARSED" | cut -d'|' -f2)
    GO_P99=$(echo "$GO_PARSED" | cut -d'|' -f4)

    GO_RESULTS[$c]="$GO_RPS|$GO_AVG|$GO_P99"

    if [ "$LEGACY_AVAILABLE" = true ]; then
        LEG_OUT=$(benchmark_endpoint "non-stream" "http://127.0.0.1:$LEGACY_PORT/v1/chat/completions" "$PAYLOAD" "$c" "$n")
        LEG_PARSED=$(parse_hey_output "$LEG_OUT")
        LEG_RPS=$(echo "$LEG_PARSED" | cut -d'|' -f1)
        LEG_AVG=$(echo "$LEG_PARSED" | cut -d'|' -f2)
        LEG_P99=$(echo "$LEG_PARSED" | cut -d'|' -f4)

        LEGACY_RESULTS[$c]="$LEG_RPS|$LEG_AVG|$LEG_P99"

        printf "%-12s | %-8s | %-20s | %-20s\n" "c=$c Go" "$GO_RPS" "${GO_AVG}s" "${GO_P99}s"
        printf "%-12s | %-8s | %-20s | %-20s\n" "c=$c JS" "$LEG_RPS" "${LEG_AVG}s" "${LEG_P99}s"
    else
        printf "%-12s | %-8s | %-20s | %-20s\n" "c=$c" "$GO_RPS" "${GO_AVG}s" "${GO_P99}s"
    fi
    echo ""
done

# Run streaming benchmark
echo -e "${BLUE}--- Streaming Chat Completions ---${NC}"
echo ""

printf "%-12s | %-8s | %-20s | %-20s\n" "Concurrent" "RPS" "Avg Latency" "P99 Latency"
printf "%-12s-+-%-8s-+-%-20s-+-%-20s\n" "------------" "--------" "--------------------" "--------------------"

for c in "${CONCURRENCY_LEVELS[@]}"; do
    n=$((c * 4))
    if [ $n -lt $REQUESTS_PER_LEVEL ]; then
        n=$REQUESTS_PER_LEVEL
    fi

    GO_OUT=$(benchmark_endpoint "stream" "http://127.0.0.1:$GO_PORT/v1/chat/completions" "$STREAM_PAYLOAD" "$c" "$n")
    GO_PARSED=$(parse_hey_output "$GO_OUT")
    GO_RPS=$(echo "$GO_PARSED" | cut -d'|' -f1)
    GO_AVG=$(echo "$GO_PARSED" | cut -d'|' -f2)
    GO_P99=$(echo "$GO_PARSED" | cut -d'|' -f4)

    if [ "$LEGACY_AVAILABLE" = true ]; then
        LEG_OUT=$(benchmark_endpoint "stream" "http://127.0.0.1:$LEGACY_PORT/v1/chat/completions" "$STREAM_PAYLOAD" "$c" "$n")
        LEG_PARSED=$(parse_hey_output "$LEG_OUT")
        LEG_RPS=$(echo "$LEG_PARSED" | cut -d'|' -f1)
        LEG_AVG=$(echo "$LEG_PARSED" | cut -d'|' -f2)
        LEG_P99=$(echo "$LEG_PARSED" | cut -d'|' -f4)

        printf "%-12s | %-8s | %-20s | %-20s\n" "c=$c Go" "$GO_RPS" "${GO_AVG}s" "${GO_P99}s"
        printf "%-12s | %-8s | %-20s | %-20s\n" "c=$c JS" "$LEG_RPS" "${LEG_AVG}s" "${LEG_P99}s"
    else
        printf "%-12s | %-8s | %-20s | %-20s\n" "c=$c" "$GO_RPS" "${GO_AVG}s" "${GO_P99}s"
    fi
    echo ""
done

# Memory comparison
echo -e "${BLUE}--- Memory Usage ---${NC}"
GO_MEM=$(ps -o rss= -p $GO_PID 2>/dev/null | awk '{printf "%.1f MB", $1/1024}')
echo "  Go Proxy RSS: $GO_MEM"
if [ "$LEGACY_AVAILABLE" = true ]; then
    LEG_MEM=$(ps -o rss= -p $LEGACY_PID 2>/dev/null | awk '{printf "%.1f MB", $1/1024}')
    echo "  Legacy (Node.js) RSS: $LEG_MEM"
fi

echo ""
echo -e "${GREEN}Benchmark complete.${NC}"

# Cleanup temp DB
rm -rf "$BENCH_DB_DIR"
