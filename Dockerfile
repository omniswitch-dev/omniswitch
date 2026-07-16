FROM golang:1.24-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/sentinel ./cmd/omniswitch
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/gateway ./cmd/gateway
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/proxy ./cmd/proxy

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/sentinel /usr/local/bin/sentinel
COPY --from=build /out/gateway /usr/local/bin/gateway
COPY --from=build /out/proxy /usr/local/bin/proxy
COPY policies ./policies
COPY examples ./examples

EXPOSE 8080
ENV OMNISWITCH_LISTEN=:8080
ENV OMNISWITCH_DATA=/data

ENTRYPOINT ["/usr/local/bin/gateway"]
