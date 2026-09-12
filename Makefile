GO_CMD=go
BINARY_NAME=boxctl

# Mark targets as phony (not files)
.PHONY: all build build-amd64 build-arm64 clean run test lint

# Default target
all: build

# Build the binary
build:
	export CGO_ENABLED=0
	$(GO_CMD) mod tidy
	$(GO_CMD) fmt ./...
	$(GO_CMD) build -ldflags="-w -s -X 'boxctl-cli/cmd.Version=`git rev-parse HEAD | cut -c1-7`' \
		-X 'boxctl-cli/cmd.BuildTime=`date -u '+%Y-%m-%d %H:%M:%S'`' \
		-X 'boxctl-cli/cmd.GoVersion=`go version`'" \
		-o $(BINARY_NAME) main.go

# Build a linux/amd64 binary (not part of the default build)
build-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO_CMD) build -ldflags="-w -s -X 'boxctl-cli/cmd.Version=`git rev-parse HEAD | cut -c1-7`' \
		-X 'boxctl-cli/cmd.BuildTime=`date -u '+%Y-%m-%d %H:%M:%S'`' \
		-X 'boxctl-cli/cmd.GoVersion=`go version`'" \
		-o $(BINARY_NAME)-amd64 main.go

# Build a darwin/arm64 binary (not part of the default build)
build-arm64:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO_CMD) build -ldflags="-w -s -X 'boxctl-cli/cmd.Version=`git rev-parse HEAD | cut -c1-7`' \
		-X 'boxctl-cli/cmd.BuildTime=`date -u '+%Y-%m-%d %H:%M:%S'`' \
		-X 'boxctl-cli/cmd.GoVersion=`go version`'" \
		-o $(BINARY_NAME)-arm64 main.go

# Clean up the binary
clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME)-amd64 $(BINARY_NAME)-arm64

# Run against a local boxctl-vms (see README's "Testing locally")
run: build
	./$(BINARY_NAME)

# Test the application
test:
	$(GO_CMD) test ./...

# Lint the application
lint:
	golangci-lint run
