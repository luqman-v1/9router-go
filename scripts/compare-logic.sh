#!/bin/bash
set -e

GO_DIR="/Users/luqmannul.hakim/gomod/project/9router-go"
JS_DIR="/Users/luqmannul.hakim/htdocs/9router"

echo "=== 9router Logic Comparison (Go vs Next.js) ==="

echo ""
echo "--- 1. COMBO STRATEGIES ---"
echo "[Go] applyComboStrategy:"
grep -A5 "switch strategy" "$GO_DIR/internal/handlers/combo.go" | head -10
echo ""
echo "[Next.js] getRotatedModels:"
grep -B2 "strategy.*round-robin\|sticky\|priority" "$JS_DIR/open-sse/services/combo.js" | head -8
echo ""
echo "[Next.js] auto-capability-switch:"
grep -A4 "autoSwitch\|reorderByCapabilities\|detectRequiredCapabilities" "$JS_DIR/open-sse/services/combo.js" | head -12

echo ""
echo "--- 2. FUSION ---"
echo "[Go] handleFusion:"
echo "  - collectPanel (goroutine + channel + grace timeout)"
echo "  - buildJudgePrompt (panel anonymized)"
echo "  - handleFusion (fan-out + judge)"
echo "[Next.js] handleFusionChat:"
echo "  - Promise.allSettled + grace timeout"
echo "  - buildJudgePrompt (same)"
echo "  - panel → judge pattern"
echo "  NOTE: Both functionally equivalent"

echo ""
echo "--- 3. HEALTH TRACKING ---"
echo "[Go] providerHealth DB:"
grep -A4 "func IsProviderHealthy" "$GO_DIR/internal/db/health.go" 2>/dev/null | head -8
echo ""
echo "[Next.js] accountFallback:"
grep -A4 "isAccountUnavailable\|checkFallbackError" "$JS_DIR/open-sse/services/accountFallback.js" | head -8

echo ""
echo "--- 4. SSE STREAM ARCHITECTURE ---"
echo "[Go] ScanStream callback:"
head -12 "$GO_DIR/internal/proxy/sse_scanner.go"
echo ""
echo "[Next.js] TransformStream:"
echo "  - createSSEStream() in stream.js"
echo "  - parseSSELine() in streamHelpers.js"
echo "  NOTE: Different impl, functionally equivalent"

echo ""
echo "=== GAPS: What Go is MISSING vs Next.js ==="
echo "1. Combo: sticky round-robin (consecutiveUseCount tracking)            [DONE]"
echo "2. Combo: auto-capability-switch (vision/pdf/search)                  [DONE]"
echo "3. Combo: transient error wait (503/502/504 cooldown)                 [DONE]"
echo "4. Combo: retry-after tracking across models                          [DONE]"
echo "5. Health: text-based error classification (patterns)                 [DONE]"
echo "6. Health: exponential backoff for rate limits                        [DONE]"
echo "7. Health: cooldown-based (not just consecutive count)                [DONE]"
echo "---"
echo "Also added:"
echo "  - Health/lock check in combo fallback loops (skip unhealthy/locked models)"
echo "  - 502/503/504 added to RetryableStatusCodes"
echo "  - Account fallback now uses classifyError() instead of fixed lock durations"
