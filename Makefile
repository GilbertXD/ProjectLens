.PHONY: run run-worker build build-worker build-migrate test test-e2e docker-e2e vet tidy docker-up docker-down

run:
	go run ./cmd/server

run-worker:
	go run ./cmd/worker

build: build-worker build-migrate
	go build -o bin/projectlens-server ./cmd/server

build-worker:
	go build -o bin/projectlens-worker ./cmd/worker

build-migrate:
	go build -o bin/projectlens-migrate ./cmd/migrate

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

test-e2e:
	@test -n "$$TEST_DATABASE_URL" || { echo "Set TEST_DATABASE_URL to a PostgreSQL test database"; exit 1; }
	go test -race -count=1 -v -run '^TestE2EDelivery$$' ./internal/integration

docker-e2e:
	bash scripts/e2e_docker.sh
