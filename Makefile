VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"
BINARY := mcp-icloud-email

.PHONY: build web server test lint clean run vet

build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY) .

web:
	cd web && npm ci && npm test && npm run build

server: web
	CGO_ENABLED=0 go build -o mail-server ./cmd/mail

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

clean:
	rm -f $(BINARY)

run: build
	./$(BINARY)

docker:
	docker build -t $(BINARY):$(VERSION) .

# Install dev tools
tools:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest

vuln:
	govulncheck ./...

all: vet lint test build
