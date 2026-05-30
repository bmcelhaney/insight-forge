# Insight Forge - Multi-stage Go build (following Stitchify Go Framework patterns)
FROM golang:1.23-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /insight-forge ./cmd/server

# Runtime image
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    libduckdb0 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /insight-forge /app/insight-forge
COPY migrations ./migrations

# Data volume for DuckDB file
VOLUME ["/app/data"]

ENV IF_ENV=production
ENV IF_PORT=8080
ENV IF_DUCKDB_PATH=/app/data/insight-forge.duckdb

EXPOSE 8080

ENTRYPOINT ["/app/insight-forge"]
