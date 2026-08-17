# Insight Forge — static Go binary + UI + ETS spreadsheet.
# The live server does not use DuckDB; do not enable CGO or install libduckdb.
FROM golang:1.26-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG COMMIT=dev
ARG BUILD_TIME=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
    -o /insight-forge ./cmd/server

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /insight-forge /app/insight-forge
COPY static ./static
COPY docs ./docs

ENV IF_ENV=production
ENV IF_PORT=8080
ENV IF_ETS_XLSX_PATH="/app/docs/20260701 AbilityOne ETS File.xlsx"

EXPOSE 8080

ENTRYPOINT ["/app/insight-forge"]
