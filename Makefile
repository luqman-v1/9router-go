BINARY_NAME := 9router-proxy
PORT := 20128
DASHBOARD_PORT := 20129
DATA_DIR := $(HOME)/.9router
MAKEFILE_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
GO_PROXY_DIR := $(MAKEFILE_DIR:/=)
ROOT_DIR := $(dir $(GO_PROXY_DIR:/=))

.PHONY: build run dev dashboard test bench clean help all

## build — compile Go proxy binary
build:
	cd $(GO_PROXY_DIR) && go build -o $(BINARY_NAME) ./cmd/9router-proxy/

## run — start Go proxy on :$(PORT)
run: build
	cd $(GO_PROXY_DIR) && PORT=$(PORT) DATA_DIR=$(DATA_DIR) ./$(BINARY_NAME)

## dev — start Go proxy with auto-rebuild
dev:
	cd $(GO_PROXY_DIR) && PORT=$(PORT) DATA_DIR=$(DATA_DIR) go run ./cmd/9router-proxy/

## dashboard — start Next.js dashboard on :$(DASHBOARD_PORT)
dashboard:
	cd $(ROOT_DIR) && node_modules/.bin/next dev --port $(DASHBOARD_PORT)

## dashboard-prod — build & start Next.js in production mode
dashboard-prod:
	cd $(ROOT_DIR) && npm run build
	cd $(ROOT_DIR) && PORT=$(DASHBOARD_PORT) NODE_ENV=production node .next/standalone/server.js

## all — build Go proxy + start both services
all: build
	@echo "Starting Go proxy on :$(PORT) and Dashboard on :$(DASHBOARD_PORT)"
	@echo "Press Ctrl+C to stop both"
	@trap 'kill 0' EXIT; \
	PORT=$(PORT) DATA_DIR=$(DATA_DIR) $(GO_PROXY_DIR)/$(BINARY_NAME) & \
	cd $(ROOT_DIR) && node_modules/.bin/next dev --port $(DASHBOARD_PORT) & \
	wait

## all-prod — build everything + start both in production mode
all-prod: build
	cd $(ROOT_DIR) && npm run build
	@echo "Starting Go proxy on :$(PORT) and Dashboard (prod) on :$(DASHBOARD_PORT)"
	@trap 'kill 0' EXIT; \
	PORT=$(PORT) DATA_DIR=$(DATA_DIR) $(GO_PROXY_DIR)/$(BINARY_NAME) & \
	cd $(ROOT_DIR) && PORT=$(DASHBOARD_PORT) NODE_ENV=production node .next/standalone/server.js & \
	wait

## test — run all Go tests
test:
	cd $(GO_PROXY_DIR) && go test ./... -v

## test-short — run tests without verbose
test-short:
	cd $(GO_PROXY_DIR) && go test ./...

## vet — run go vet
vet:
	cd $(GO_PROXY_DIR) && go vet ./...

## bench — run benchmark comparison (Go vs Next.js)
bench: build
	cd $(GO_PROXY_DIR) && bash benchmark/run_comparison.sh

## bench-go — run Go-only benchmark
bench-go: build
	go run benchmark/mock_upstream.go &
	@sleep 2
	@echo "Benchmarking Go proxy..."
	@hey -n 200 -c 10 -m POST \
		-H "Authorization: Bearer sk-benchmark-test-key" \
		-H "Content-Type: application/json" \
		-d '{"model":"mock/mock-model","messages":[{"role":"user","content":"Hello"}],"stream":false,"max_tokens":10}' \
		http://127.0.0.1:20199/v1/chat/completions
	@kill %1 2>/dev/null || true

## cross — cross-compile for Linux, Windows, macOS ARM
cross:
	cd $(GO_PROXY_DIR) && GOOS=linux GOARCH=amd64 go build -o $(BINARY_NAME)-linux-amd64 ./cmd/9router-proxy/
	cd $(GO_PROXY_DIR) && GOOS=linux GOARCH=arm64 go build -o $(BINARY_NAME)-linux-arm64 ./cmd/9router-proxy/
	cd $(GO_PROXY_DIR) && GOOS=darwin GOARCH=arm64 go build -o $(BINARY_NAME)-darwin-arm64 ./cmd/9router-proxy/
	cd $(GO_PROXY_DIR) && GOOS=windows GOARCH=amd64 go build -o $(BINARY_NAME)-windows-amd64.exe ./cmd/9router-proxy/
	@echo "Cross-compiled binaries:"
	@ls -lh $(GO_PROXY_DIR)/$(BINARY_NAME)-*

## clean — remove built binaries
clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME)-*

## help — show this help
help:
	@echo "9Router Go Proxy — Makefile targets:"
	@echo ""
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  make /' | sed 's/ — /  /'
	@echo ""
	@echo "Ports: Go proxy=$(PORT), Dashboard=$(DASHBOARD_PORT)"
	@echo "Data:  $(DATA_DIR)"
