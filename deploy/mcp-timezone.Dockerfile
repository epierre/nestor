# Built from the repository root: docker compose -f deploy/docker-compose.yml build mcp-timezone
FROM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY go.* ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /mcp-timezone ./cmd/mcp-timezone

# No shell, no CA certificates: the server never calls out, and its time zone data is embedded.
FROM scratch
COPY --from=build /mcp-timezone /mcp-timezone
USER 65532:65532
ENTRYPOINT ["/mcp-timezone"]
