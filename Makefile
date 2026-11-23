.PHONY: run run-worker build build-worker test vet tidy docker-up docker-down

run:
	go run ./cmd/server

run-worker:
	go run ./cmd/worker

build: build-worker
	go build -o bin/projectlens-server ./cmd/server

build-worker:
	go build -o bin/projectlens-worker ./cmd/worker

test:
	go test -race -v ./...

vet:
	go vet ./...

tidy:
	go mod tidy

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down

.PHONY: migrate-up migrate-down migrate-status test-integration

migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down

migrate-status:
	go run ./cmd/migrate status

test-integration:
	go test -race -v ./internal/integration
