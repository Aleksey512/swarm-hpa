# syntax=docker/dockerfile:1

# ---- build stage ---------------------------------------------------------
FROM golang:1.25-alpine AS build

# VERSION is embedded into the binary via -ldflags (defaults to "dev").
# TARGETOS/TARGETARCH are auto-populated by BuildKit (declared bare). When a
# builder does not provide them, empty GOOS/GOARCH fall back to a native build.
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Cache module downloads separately from the source for faster rebuilds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO-free static build; -trimpath + -s -w keep the binary small and reproducible.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/swarm-hpa ./cmd/swarm-hpa

# ---- runtime stage -------------------------------------------------------
FROM alpine:3.20

# ca-certificates lets the daemon reach a TLS Prometheus / Docker endpoint.
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates \
    && addgroup -S swarmhpa \
    && adduser -S -G swarmhpa -u 65532 swarmhpa

COPY --from=build /out/swarm-hpa /usr/local/bin/swarm-hpa

# Run as non-root; the daemon writes nothing to disk (safe for a read-only rootfs).
USER swarmhpa

# Reinforce the safety default so a bare `docker run` never mutates a live Swarm.
ENV DRY_RUN=true

# The daemon's own Prometheus endpoint (see METRICS_ADDR, default :9095).
EXPOSE 9095

# Healthy when the metrics endpoint serves. NOTE: the port is hard-coded to the
# :9095 default — adjust if you override METRICS_ADDR.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:9095/metrics >/dev/null 2>&1 || exit 1

ENTRYPOINT ["swarm-hpa"]
