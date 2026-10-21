BINARY_NAME := gateway
PKG := ./...

.PHONY: all build run test race bench vet fmt lint docker-up docker-down clean

all: fmt vet test build

build:
	go build -trimpath -ldflags="-s -w" -o bin/$(BINARY_NAME) ./cmd/gateway

run:
	go run ./cmd/gateway -config configs/gateway.yaml

test:
	go test -v $(PKG)

race:
	go test -v -race $(PKG)

bench:
	go test -bench=. -benchmem $(PKG)

vet:
	go vet $(PKG)

fmt:
	gofmt -s -w .

lint:
	golangci-lint run ./...

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down

clean:
	rm -rf bin/ coverage.out