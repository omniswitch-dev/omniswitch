# Minimal, dependency-free build for the deterministic mock upstream used
# by the OmniSwitch competitive benchmark harness (see bench/README.md).
# Build context is bench/ (see bench/docker-compose.yml).
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd/mock-upstream ./cmd/mock-upstream
RUN CGO_ENABLED=0 go build -trimpath -o /out/mock-upstream ./cmd/mock-upstream

FROM alpine:3.20
COPY --from=build /out/mock-upstream /mock-upstream
EXPOSE 4000
ENTRYPOINT ["/mock-upstream"]
