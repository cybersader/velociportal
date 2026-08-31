.PHONY: build fmt-check test vet lint check run docker validate-env-file docker-run setup observe-proxy doctor validate validate-json up logs health down production-compose-check verify clean
.NOTPARALLEL: check verify

PYTHON ?= python3
IMAGE ?= velociportal:latest
BUILD_VERSION ?= dev
GIT_REVISION ?= $(shell git rev-parse --verify HEAD 2>/dev/null || printf unknown)
GIT_SOURCE_STATE ?= $(shell if [ -z "$$(git status --porcelain --untracked-files=normal 2>/dev/null)" ]; then printf clean; else printf dirty; fi)
BUILD_LDFLAGS = -s -w -X main.buildVersion=$(BUILD_VERSION) -X main.buildRevision=$(GIT_REVISION) -X main.buildSourceState=$(GIT_SOURCE_STATE)
ENV_FILE ?= .env
CONTAINER_ENV_FILE ?= /workspace/$(ENV_FILE)
HEALTH_URL ?= http://127.0.0.1:8080/healthz
DOCTOR_ARGS ?=
VALIDATE_ARGS ?=
VELOCIPORTAL_SUBNET ?= 172.31.255.0/24
VELOCIPORTAL_GATEWAY ?= 172.31.255.1
HOST_UID ?= $(shell id -u)
HOST_GID ?= $(shell id -g)
DOCKER_ROOTLESS ?= $(shell docker info --format '{{json .SecurityOptions}}' 2>/dev/null | grep -q rootless && printf 1)
CONTAINER_UID ?= $(if $(DOCKER_ROOTLESS),0,$(HOST_UID))
CONTAINER_GID ?= $(if $(DOCKER_ROOTLESS),0,$(HOST_GID))
PRIVATE_CA_FILE ?=
SERVICE_METADATA_FILE ?=
SERVICE_METADATA_GID ?=
SERVICE_HEALTH_FILE ?=
SERVICE_HEALTH_GID ?=

PRIVATE_CA_COMPOSE = $(if $(strip $(PRIVATE_CA_FILE)),-f docker-compose.private-ca.yml)
SERVICE_METADATA_COMPOSE = $(if $(strip $(SERVICE_METADATA_FILE)),-f docker-compose.service-metadata.yml)
SERVICE_HEALTH_COMPOSE = $(if $(strip $(SERVICE_HEALTH_FILE)),-f docker-compose.service-health.yml)
COMPOSE_ENV = IMAGE="$(IMAGE)" BUILD_VERSION="$(BUILD_VERSION)" GIT_REVISION="$(GIT_REVISION)" GIT_SOURCE_STATE="$(GIT_SOURCE_STATE)" VELOCIPORTAL_SUBNET="$(VELOCIPORTAL_SUBNET)" VELOCIPORTAL_GATEWAY="$(VELOCIPORTAL_GATEWAY)" PRIVATE_CA_FILE="$(PRIVATE_CA_FILE)" VELOCIPORTAL_SERVICE_METADATA_FILE="$(SERVICE_METADATA_FILE)" VELOCIPORTAL_SERVICE_METADATA_GID="$(SERVICE_METADATA_GID)" VELOCIPORTAL_SERVICE_HEALTH_FILE="$(SERVICE_HEALTH_FILE)" VELOCIPORTAL_SERVICE_HEALTH_GID="$(SERVICE_HEALTH_GID)"
COMPOSE = ENV_FILE="$(ENV_FILE)" $(COMPOSE_ENV) docker compose -f docker-compose.yml $(PRIVATE_CA_COMPOSE) $(SERVICE_METADATA_COMPOSE) $(SERVICE_HEALTH_COMPOSE)
SETUP_COMPOSE = ENV_FILE=".env.example" $(COMPOSE_ENV) docker compose -f docker-compose.yml $(PRIVATE_CA_COMPOSE) $(SERVICE_METADATA_COMPOSE) $(SERVICE_HEALTH_COMPOSE)
WORKSPACE_VOLUME = $(CURDIR):/workspace

build:
	go build -ldflags="$(BUILD_LDFLAGS)" -o velociportal .

fmt-check:
	@unformatted="$$(gofmt -l *.go)"; \
	if [ -n "$$unformatted" ]; then \
		printf 'gofmt required for:\n%s\n' "$$unformatted"; \
		exit 1; \
	fi

test:
	go test -v -race -count=1 ./...

vet:
	go vet ./...

lint: fmt-check vet

check: fmt-check vet test

# Run locally without executing the environment file as shell code.
run:
	go run -ldflags="$(BUILD_LDFLAGS)" . serve --env-file "$(ENV_FILE)"

# Build the production scratch image.
docker:
	docker build \
		--build-arg BUILD_VERSION="$(BUILD_VERSION)" \
		--build-arg GIT_REVISION="$(GIT_REVISION)" \
		--build-arg GIT_SOURCE_STATE="$(GIT_SOURCE_STATE)" \
		-t "$(IMAGE)" .

# Containerized tools address the same project-relative file on the host and at
# /workspace inside the container. Absolute and parent-relative paths would refer
# to different files, so reject them instead of silently splitting configuration.
validate-env-file:
	@case "$(ENV_FILE)" in \
		/*|..|../*|*/../*|*/..) \
			printf 'ENV_FILE must be a project-relative path without .. components: %s\n' "$(ENV_FILE)" >&2; \
			exit 2 ;; \
	esac

# Run the production service as a one-off Compose container so the same raw,
# literal env-file semantics and loopback-only publication are used.
docker-run: validate-env-file docker
	$(COMPOSE) run --rm --no-deps --service-ports velociportal

# Setup and proxy observation may update ENV_FILE, so only these wrappers receive
# a writable project bind mount. Native Docker uses host IDs; rootless Docker uses
# container root, which maps back to the unprivileged daemon owner on the host.
setup: validate-env-file docker
	$(SETUP_COMPOSE) run --rm --no-deps --interactive \
		--user "$(CONTAINER_UID):$(CONTAINER_GID)" \
		--volume "$(WORKSPACE_VOLUME)" \
		--workdir /workspace \
		velociportal-tools setup --env-file "$(CONTAINER_ENV_FILE)"

# Run the observer on the same Compose network the production service will use.
observe-proxy: validate-env-file docker
	$(COMPOSE) run --rm --no-deps --interactive --service-ports \
		--user "$(CONTAINER_UID):$(CONTAINER_GID)" \
		--volume "$(WORKSPACE_VOLUME)" \
		--workdir /workspace \
		velociportal-tools setup observe-proxy --env-file "$(CONTAINER_ENV_FILE)" --listen 0.0.0.0:8080

# Doctor reads the selected file through a read-only mount and uses the same
# Compose network as the production service.
doctor: validate-env-file docker
	$(COMPOSE) run --rm --no-deps --no-TTY \
		--user "$(CONTAINER_UID):$(CONTAINER_GID)" \
		--volume "$(WORKSPACE_VOLUME):ro" \
		velociportal-tools doctor --env-file "$(CONTAINER_ENV_FILE)" $(DOCTOR_ARGS)

# Validation uses the same read-only configuration and production network as
# doctor, but returns nonzero when the report still needs operator review.
validate: validate-env-file docker
	@$(COMPOSE) run --rm --no-deps --no-TTY \
		--user "$(CONTAINER_UID):$(CONTAINER_GID)" \
		--volume "$(WORKSPACE_VOLUME):ro" \
		velociportal-tools validate --env-file "$(CONTAINER_ENV_FILE)" $(VALIDATE_ARGS)

# Keep stdout machine-readable so `make validate-json > report.json` captures only
# the report. Image-build progress and Compose diagnostics remain on stderr.
validate-json: validate-env-file
	@$(MAKE) --no-print-directory docker >&2
	@$(COMPOSE) run --rm --no-deps --no-TTY \
		--user "$(CONTAINER_UID):$(CONTAINER_GID)" \
		--volume "$(WORKSPACE_VOLUME):ro" \
		velociportal-tools validate --env-file "$(CONTAINER_ENV_FILE)" $(VALIDATE_ARGS) --format json

# Build once, then start that exact image and wait for Docker health to pass.
up: validate-env-file docker
	$(COMPOSE) up --detach --wait --no-build

logs: validate-env-file
	$(COMPOSE) logs --follow velociportal

health: validate-env-file
	$(COMPOSE) exec -T velociportal /velociportal healthcheck --url "$(HEALTH_URL)"

down: validate-env-file
	$(COMPOSE) down

# Verify the portable one-service bundle independently of a Docker daemon.
production-compose-check:
	$(PYTHON) scripts/verify-production-compose.py

# Full local verification, including contributor Compose, the production bundle,
# raw env semantics, and image metadata.
verify: fmt-check vet test docker production-compose-check
	ENV_FILE=.env.example IMAGE="$(IMAGE)" docker compose -f docker-compose.yml --profile tools config --quiet
	ENV_FILE=.env.example IMAGE="$(IMAGE)" PRIVATE_CA_FILE="$(CURDIR)/.env.example" docker compose -f docker-compose.yml -f docker-compose.private-ca.yml --profile tools config --quiet
	ENV_FILE=.env.example IMAGE="$(IMAGE)" VELOCIPORTAL_SERVICE_METADATA_FILE="$(CURDIR)/deploy/service-metadata.example.json" VELOCIPORTAL_SERVICE_METADATA_GID=950 docker compose -f docker-compose.yml -f docker-compose.service-metadata.yml --profile tools config --quiet
	ENV_FILE=.env.example IMAGE="$(IMAGE)" VELOCIPORTAL_SERVICE_HEALTH_FILE="$(CURDIR)/deploy/service-health.example.json" VELOCIPORTAL_SERVICE_HEALTH_GID=951 docker compose -f docker-compose.yml -f docker-compose.service-health.yml --profile tools config --quiet
	ENV_FILE=.env.example IMAGE="$(IMAGE)" PRIVATE_CA_FILE="$(CURDIR)/.env.example" VELOCIPORTAL_SERVICE_METADATA_FILE="$(CURDIR)/deploy/service-metadata.example.json" VELOCIPORTAL_SERVICE_METADATA_GID=950 docker compose -f docker-compose.yml -f docker-compose.private-ca.yml -f docker-compose.service-metadata.yml --profile tools config --quiet
	ENV_FILE=.env.example IMAGE="$(IMAGE)" PRIVATE_CA_FILE="$(CURDIR)/.env.example" VELOCIPORTAL_SERVICE_HEALTH_FILE="$(CURDIR)/deploy/service-health.example.json" VELOCIPORTAL_SERVICE_HEALTH_GID=951 docker compose -f docker-compose.yml -f docker-compose.private-ca.yml -f docker-compose.service-health.yml --profile tools config --quiet
	ENV_FILE=.env.example IMAGE="$(IMAGE)" VELOCIPORTAL_SERVICE_METADATA_FILE="$(CURDIR)/deploy/service-metadata.example.json" VELOCIPORTAL_SERVICE_METADATA_GID=950 VELOCIPORTAL_SERVICE_HEALTH_FILE="$(CURDIR)/deploy/service-health.example.json" VELOCIPORTAL_SERVICE_HEALTH_GID=951 docker compose -f docker-compose.yml -f docker-compose.service-metadata.yml -f docker-compose.service-health.yml --profile tools config --quiet
	ENV_FILE=.env.example IMAGE="$(IMAGE)" PRIVATE_CA_FILE="$(CURDIR)/.env.example" VELOCIPORTAL_SERVICE_METADATA_FILE="$(CURDIR)/deploy/service-metadata.example.json" VELOCIPORTAL_SERVICE_METADATA_GID=950 VELOCIPORTAL_SERVICE_HEALTH_FILE="$(CURDIR)/deploy/service-health.example.json" VELOCIPORTAL_SERVICE_HEALTH_GID=951 docker compose -f docker-compose.yml -f docker-compose.private-ca.yml -f docker-compose.service-metadata.yml -f docker-compose.service-health.yml --profile tools config --quiet
	@test "$$(docker image inspect --format '{{json .Config.Healthcheck.Test}}' "$(IMAGE)")" = \
		'["CMD","/velociportal","healthcheck"]' || { \
		printf 'image %s does not contain the expected healthcheck command\n' "$(IMAGE)"; \
		exit 1; \
	}
	@test "$$(docker image inspect --format '{{.Config.User}}' "$(IMAGE)")" = '65534:65534' || { \
		printf 'image %s does not run as the expected non-root user\n' "$(IMAGE)"; \
		exit 1; \
	}
	docker run --rm --read-only --security-opt no-new-privileges \
		"$(IMAGE)" healthcheck --help >/dev/null
	docker run --rm --read-only --security-opt no-new-privileges \
		"$(IMAGE)" validate --help >/dev/null
	docker run --rm --read-only --security-opt no-new-privileges \
		"$(IMAGE)" suggest-hostnames --help >/dev/null

clean:
	rm -f velociportal
