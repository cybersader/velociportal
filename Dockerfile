FROM golang:1.22-alpine AS builder
RUN apk add --no-cache ca-certificates
WORKDIR /build
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o velociportal .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/velociportal /velociportal

# Run as an unprivileged user. Linux enforces numeric UIDs without needing an
# /etc/passwd entry, so 65534:65534 (nobody:nogroup) works on `FROM scratch`.
USER 65534:65534

EXPOSE 8080

# No HEALTHCHECK here: the `FROM scratch` image contains only the static binary
# and CA certs — there is no shell, wget, or curl to run a probe. Health is
# exposed at the HTTP /healthz endpoint (503 while the cache is empty/stale);
# probe it from your reverse proxy or an external monitor. See docker-compose.yml.

ENTRYPOINT ["/velociportal"]
