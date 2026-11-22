.PHONY: run build test vet tidy docker-up docker-down

run:
	go run ./cmd/server

build:
	go build -o bin/projectlens-server ./cmd/server

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
