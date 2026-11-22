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
