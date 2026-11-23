# Built from the repository root: docker compose -f deploy/docker-compose.yml build sandboxd
FROM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY go.* ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /sandboxd ./cmd/sandboxd

# Runs as root with every capability dropped (see docker-compose.yml): that is what opening the
# root-owned Docker socket needs, and nothing else.
FROM scratch
COPY --from=build /sandboxd /sandboxd
USER 0:0
ENTRYPOINT ["/sandboxd"]
