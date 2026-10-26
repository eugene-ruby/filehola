.PHONY: test integration check-api build up down

test:
	go test -race ./...

integration:
	docker compose up -d --wait postgres minio
	docker compose run --rm bucket
	go test -race -tags=integration ./... -count=1

check-api:
	go run ./tools/openapi

build:
	go build -trimpath -o bin/filehola ./cmd/filehola

up:
	docker compose up -d --build

down:
	docker compose down
