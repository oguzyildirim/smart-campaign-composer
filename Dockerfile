# syntax=docker/dockerfile:1
# Builds any Go binary in this repository. Select it with --build-arg:
#   CMD_PATH=./cmd/api | ./cmd/worker | ./mock-provider
FROM golang:1.27.3-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY mock-provider ./mock-provider
ARG CMD_PATH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    test -n "$CMD_PATH" && go build -trimpath -ldflags="-s -w" -o /out/app "$CMD_PATH"

# Distroless: no shell or package manager; runs as an unprivileged user.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
