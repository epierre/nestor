# Built from the repository root: docker compose -f deploy/docker-compose.yml build nestor
FROM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.* ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /nestor ./cmd/nestor

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /nestor /nestor
USER 65534:65534
ENTRYPOINT ["/nestor"]
