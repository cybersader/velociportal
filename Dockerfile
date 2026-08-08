FROM golang:1.22-alpine@sha256:1699c10032ca2582ec89a24a1312d986a3f094aed3d5c1147b19880afe40e052 AS builder
ARG BUILD_VERSION=dev
ARG GIT_REVISION=unknown
ARG GIT_SOURCE_STATE=unknown
RUN apk add --no-cache ca-certificates
WORKDIR /build
COPY go.mod go.sum* ./
RUN go mod download
# Allowlist build inputs so arbitrary local configuration files never enter the
# Docker context layer or builder cache.
COPY *.go ./
COPY assets ./assets
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.buildVersion=${BUILD_VERSION} -X main.buildRevision=${GIT_REVISION} -X main.buildSourceState=${GIT_SOURCE_STATE}" \
    -o velociportal .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/velociportal /velociportal

# Run as an unprivileged user. Linux enforces numeric UIDs without needing an
# /etc/passwd entry, so 65534:65534 (nobody:nogroup) works on `FROM scratch`.
USER 65534:65534

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=40s --retries=3 \
    CMD ["/velociportal", "healthcheck"]

ENTRYPOINT ["/velociportal"]
