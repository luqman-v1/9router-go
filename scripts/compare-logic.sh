#!/bin/bash
# compare-logic.sh — compare Go vs Next.js 9router logic
set -e

GO_DIR="/Users/luqmannul.hakim/gomod/project/9router-go"
JS_DIR="/Users/luqmannul.hakim/htdocs/9router"

echo "=== 9router Logic Comparison (Go vs Next.js) ==="
echo ""

echo "--- 1. COMBO STRATEGIES ---"
echo "[Go] supported:"
grep -A2 "switch strategy" "$GO_DIR/internal/handlers/combo.go" | head -6
echo "[Next.js] supported:"
grep -A5 "getRotatedModels" "$JS_DIR/open-sse/services/combo.js" | head -8
echo ""

echo "--- 2. SSE[ DONE ] HANDLING ---"
echo "[Go] translator:"
grep -n "DONE\|isDone" "$GO_DIR/internal/translator/response.go" | head -6
echo "[Next.js] streamHelpers:"
grep -n "DONE\|done" "$JS_DIR/open-sse/utils/streamHelpers.js" | head -6
echo ""

echo "--- 3. FUSION ---"
echo "[Go] handleFusion:"
grep -n "func.*[Ff]usion\|panic\|select {" "$GO_DIR/internal/handlers/combo.go" | head -8
echo "[Next.js] handleFusionChat:"
grep -n "FusionChat\|minPanel\.\|timeout" "$JS_DIR/open-sse/services/combo.js" | head -8
echo ""

echo "--- 4. HEALTH TRACKING ---"
echo "[Go] IsProviderHealthy:"
grep -A3 "func IsProviderHealthy" "$GO_DIR/internal/db/health.go" 2>/dev/null | head -6
echo "[Next.js] isAccountUnavailable:"
grep -A3 "isAccountUnavailable" "$JS_DIR/open-sse/services/accountFallback.js" | head -6
