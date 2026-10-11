FROM golang:1.24-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/omniswitch ./cmd/gateway
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/omniswitch-policy ./cmd/omniswitch
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/proxy ./cmd/proxy
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
