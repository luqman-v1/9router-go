BINARY_NAME := 9router-go
PORT ?= 20128
DATA_DIR ?= $(HOME)/.9router
RTK ?= true
CAVEMAN ?= false
PONYTAIL ?= false

.PHONY: build run dev test vet bench cross docker docker-build clean help

## build — compile binary
build:
	go build -ldflags="-s -w" -o $(BINARY_NAME) ./cmd/9router-proxy/

## run — start proxy (PORT=20128)
run: build
	PORT=$(PORT) DATA_DIR=$(DATA_DIR) ./$(BINARY_NAME) --rtk=$(RTK) --caveman=$(CAVEMAN) --ponytail=$(PONYTAIL)

## dev — start with go run (auto-rebuild)
dev:
	PORT=$(PORT) DATA_DIR=$(DATA_DIR) go run ./cmd/9router-proxy/

## test — run all tests
test:
	go test ./... -v

## test-short — run tests (quiet)
test-short:
	go test ./...

## vet — go vet
vet:
	go vet ./...

## bench — run benchmark
bench: build
	bash benchmark/run_comparison.sh

## cross — cross-compile Linux/macOS/Windows
cross:
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(BINARY_NAME)-linux-amd64 ./cmd/9router-proxy/
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o $(BINARY_NAME)-linux-arm64 ./cmd/9router-proxy/
	GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o $(BINARY_NAME)-darwin-arm64 ./cmd/9router-proxy/
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o $(BINARY_NAME)-windows-amd64.exe ./cmd/9router-proxy/
	@ls -lh $(BINARY_NAME)-*

## docker — docker compose up
docker:
	docker compose up -d

## docker-build — build Docker image only
docker-build:
	docker build -t $(BINARY_NAME) .

## clean — remove binaries
clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME)-*

## help — show targets
help:
	@echo "9router-go — targets:"
	@grep -E '^## ' Makefile | sed 's/## /  make /' | sed 's/ — /  /'
	@echo ""
	@echo "Custom:  make run PORT=3000"
	@echo "         make run DATA_DIR=/path/to/data"
	@echo ""
	@echo "Token savers:  make run CAVEMAN=true PONYTAIL=true"
	@echo "               make run RTK=false"
	@echo "               make run CAVEMAN=true PONYTAIL=true PORT=3000"
	@echo "               make run RTK=$(RTK) CAVEMAN=$(CAVEMAN) PONYTAIL=$(PONYTAIL)"
