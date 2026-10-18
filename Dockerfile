# Pin the patch release: the golang image sets GOTOOLCHAIN=local, so a stale
# cached golang:1.26 tag would build with an older 1.26.x and ignore the
# `toolchain go1.26.9` line in go.mod that the vulnerability fixes rely on.
# Keep this in sync with the toolchain line.
FROM golang:1.26.9-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Build metadata reported by `omniswitch version` and the startup log, e.g.
#   docker build --build-arg VERSION=0.2.0 --build-arg COMMIT=$(git rev-parse --short HEAD) \
#     --build-arg DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) .
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/omniswitch ./cmd/gateway
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/omniswitch-policy ./cmd/omniswitch
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/proxy ./cmd/proxy
# Distroless has no shell, so create the data directory here and copy it with
# the nonroot owner (65532). Without it the gateway cannot create /data.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/omniswitch /usr/local/bin/omniswitch
COPY --from=build /out/omniswitch-policy /usr/local/bin/omniswitch-policy
COPY --from=build /out/proxy /usr/local/bin/proxy
COPY policies ./policies
COPY examples ./examples
COPY --from=build --chown=65532:65532 /out/data /data

EXPOSE 8080
ENV OMNISWITCH_LISTEN=:8080
ENV OMNISWITCH_DATA=/data

ENTRYPOINT ["/usr/local/bin/omniswitch"]
