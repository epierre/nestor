# Image for skill scripts, started only by sandboxd, one throwaway container per run.
# Built from the repository root: docker compose -f deploy/docker-compose.yml --profile build build skill-runner
FROM python:3.13-alpine@sha256:9ef1a599f863231c3d91e941389bfdc4a2d6070557243d54df1a4e57015b960e
COPY deploy/skill-runner/entrypoint.sh /usr/local/bin/nestor-run
USER 65532:65532
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/nestor-run"]
