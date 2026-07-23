.PHONY: build test vet lint check run docker docker-run clean

IMAGE ?= velociportal:latest

build:
	go build -o velociportal .

test:
	go test -v -race -count=1 ./...

vet:
	go vet ./...

lint: vet

check: vet test

# Run locally, loading env vars from .env if present.
run:
	@if [ -f .env ]; then set -a; . ./.env; set +a; fi; go run .

# Build the Docker image.
docker:
	docker build -t $(IMAGE) .

# Run the Docker image, loading env vars from .env.
docker-run:
	docker run --rm --read-only --env-file .env -p 8080:8080 $(IMAGE)

clean:
	rm -f velociportal
