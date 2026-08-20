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
    curl \
    gnupg \
    iproute2 \
    iptables \
    iputils-ping \
    python3 \
    wireguard-tools \
    && curl -fsSL https://pkgs.netbird.io/debian/public.key | gpg --dearmor -o /usr/share/keyrings/netbird-archive-keyring.gpg \
    && echo 'deb [signed-by=/usr/share/keyrings/netbird-archive-keyring.gpg] https://pkgs.netbird.io/debian stable main' \
        > /etc/apt/sources.list.d/netbird.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends netbird \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /insight-forge /app/insight-forge
COPY static ./static
COPY docs ./docs
COPY docker/entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY docker/nb-ch-route.sh /usr/local/bin/nb-ch-route.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh /usr/local/bin/nb-ch-route.sh

ENV IF_ENV=production
ENV IF_PORT=8080
ENV IF_ETS_XLSX_PATH="/app/docs/20260701 AbilityOne ETS File.xlsx"

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/app/insight-forge"]
