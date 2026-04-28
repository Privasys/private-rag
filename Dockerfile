# private-rag - Per-fleet TDX enclave server
#
# Reproducible OCI image built for use inside Enclave OS Virtual.
# The image must be byte-for-byte identical across builds from the same
# source tree so that operators can pin its `@sha256:...` digest in the
# Enclave OS Virtual workload manifest and clients can verify it via the
# per-container OID extensions in the RA-TLS certificate.

# --- Build stage --------------------------------------------------------
FROM golang:1.22-bookworm AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .

# -trimpath strips the local build path; -s -w drops debug + symbol tables.
# Both are required for a reproducible binary.
RUN CGO_ENABLED=0 go build \
        -trimpath \
        -ldflags='-s -w' \
        -o /private-rag \
        ./cmd/private-rag

# --- Runtime stage ------------------------------------------------------
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /private-rag /usr/local/bin/private-rag

EXPOSE 8443

ENTRYPOINT ["private-rag"]
