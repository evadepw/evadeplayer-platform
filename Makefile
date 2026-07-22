.PHONY: up up-no-transcoder down restart build api-build transcoder-build \
        logs logs-api logs-transcoder ps clean \
        worker-up worker-up-nvidia worker-up-vaapi worker-down worker-rebuild worker-logs \
        migrate migrate-down migrate-local \
        test test-verbose lint

# --- Single server (all services on one machine) ------------------------------
# The transcoder starts only when COMPOSE_PROFILES contains "transcoder"
# (setup.sh sets this automatically for all-in-one mode).

up:
	docker compose up -d

# Start without the transcoder (overrides COMPOSE_PROFILES from .env)
up-no-transcoder:
	COMPOSE_PROFILES=$$(echo "$${COMPOSE_PROFILES}" | tr ',' '\n' | grep -v '^transcoder$$' | paste -sd ',') docker compose up -d

down:
	docker compose down

restart:
	docker compose restart

build:
	docker compose build

api-build:
	docker compose build api

transcoder-build:
	docker compose build transcoder

logs:
	docker compose logs -f

logs-api:
	docker compose logs -f api

logs-transcoder:
	docker compose logs -f transcoder

ps:
	docker compose ps

clean:
	docker compose down -v --remove-orphans

# --- Dedicated worker server --------------------------------------------------
# Run ./setup.sh on the worker machine first (choose "worker") — it writes .env
# with the main server address and picks the right compose files.

worker-up:
	@if grep -q '^TRANSCODE_ACCEL=nvidia$$' .env 2>/dev/null; then \
		docker compose -f docker-compose.worker.yml -f docker-compose.nvidia.yml up -d; \
	elif grep -q '^TRANSCODE_ACCEL=vaapi$$' .env 2>/dev/null; then \
		docker compose -f docker-compose.worker.yml -f docker-compose.vaapi.yml up -d; \
	else \
		docker compose -f docker-compose.worker.yml up -d; \
	fi

worker-up-nvidia:
	docker compose -f docker-compose.worker.yml -f docker-compose.nvidia.yml up -d

worker-up-vaapi:
	docker compose -f docker-compose.worker.yml -f docker-compose.vaapi.yml up -d

worker-down:
	docker compose -f docker-compose.worker.yml down

worker-rebuild:
	docker compose -f docker-compose.worker.yml up -d --build

worker-logs:
	docker compose -f docker-compose.worker.yml logs -f

# --- Database migrations ------------------------------------------------------

migrate:
	docker compose run --rm migrate

migrate-down:
	docker compose run --rm migrate \
		-path=/migrations \
		-database="postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}?sslmode=disable" \
		down 1

migrate-local:
	migrate -path ./migrations \
		-database "postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@localhost:5432/$(POSTGRES_DB)?sslmode=disable" \
		up

# --- Tests & lint -------------------------------------------------------------
# Run in a container so a local Go toolchain is not required.

test:
	docker run --rm -v $(shell pwd):/workspace -w /workspace \
		golang:1.25-alpine go test ./...

test-verbose:
	docker run --rm -v $(shell pwd):/workspace -w /workspace \
		golang:1.25-alpine go test -v ./...

lint:
	docker run --rm -v $(shell pwd):/workspace -w /workspace \
		golangci/golangci-lint:v2.1.6 golangci-lint run
