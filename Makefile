.PHONY: run run/api run-dev build test audit tidy fmt lint clean db/migrations/new db/migrations/up db/migrations/down db/migrations/version docker-build docker-up docker-down docker-logs

# Load .env (if present) so DATABASE_URL etc. are available to targets.
-include .env
export

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

## run/api: run the API server
run/api:
	go run -ldflags "$(LDFLAGS)" ./app/server

## run: alias for run/api
run: run/api

## run-dev: run with hot reload (requires air)
run-dev:
	air

## build: build the binary
build:
	go build -ldflags "$(LDFLAGS)" -o bin/doexam ./app/server

## test: run all tests
test:
	go test ./... -v -count=1

## audit: tidy, format, vet, and test
audit:
	@echo 'Tidying and verifying module dependencies...'
	go mod tidy
	go mod verify
	@echo 'Formatting code...'
	go fmt ./...
	@echo 'Vetting code...'
	go vet ./...
	@echo 'Running tests...'
	go test -race -vet=off ./...

## tidy: tidy go modules
tidy:
	go mod tidy

## fmt: format code
fmt:
	go fmt ./...

## lint: run linter (requires golangci-lint)
lint:
	golangci-lint run ./...

## clean: remove build artifacts
clean:
	rm -rf bin/

## db/migrations/new name=$1: create a new migration (requires golang-migrate)
db/migrations/new:
	@echo 'Creating migration files for ${name}...'
	migrate create -seq -ext=.sql -dir=./migrations ${name}

## db/migrations/up: apply all up migrations (embedded in the binary; no migrate CLI needed)
db/migrations/up:
	go run ./app/server migrate up

## db/migrations/down: roll back the last migration
db/migrations/down:
	go run ./app/server migrate down 1

## db/migrations/version: show the current migration version
db/migrations/version:
	go run ./app/server migrate version

## docker-build: build docker image
docker-build:
	docker compose build

## docker-up: start containers
docker-up:
	docker compose up -d

## docker-down: stop containers
docker-down:
	docker compose down

## docker-logs: tail API logs
docker-logs:
	docker compose logs -f api
