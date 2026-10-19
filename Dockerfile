FROM --platform=$BUILDPLATFORM node:22.22.0-bookworm-slim AS frontend
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@10.28.2 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.27.2-bookworm AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/web/dist ./web/dist
ARG DOCKLANE_VERSION=0.3.2-beta.2
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/docker-manager/docker-manager/internal/buildinfo.Version=$DOCKLANE_VERSION" -o /out/docklane ./cmd/server

FROM docker:28.5.1-cli AS docker-cli
FROM docker/compose-bin:v2.40.3 AS compose
FROM restic/restic:0.19.1 AS restic

FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS runtime-assets
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/* && mkdir -p /image-data /image-backups

FROM debian:bookworm-slim
ARG DOCKLANE_VERSION=0.3.2-beta.2
COPY LICENSE /usr/share/licenses/docklane/LICENSE
COPY --from=runtime-assets /etc/ssl/certs /etc/ssl/certs
COPY --from=runtime-assets /usr/share/ca-certificates /usr/share/ca-certificates
COPY --from=runtime-assets /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=runtime-assets /image-data /data
COPY --from=runtime-assets /image-backups /backups
COPY --from=backend /out/docklane /usr/local/bin/docklane
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=compose /docker-compose /usr/local/lib/docker/cli-plugins/docker-compose
COPY --from=restic /usr/bin/restic /usr/local/bin/restic
ENV DM_MODE=docker DM_DATA_DIR=/data DM_LISTEN=:9999 DM_BACKUP_WORKER_IMAGE=docklane:v${DOCKLANE_VERSION}
EXPOSE 9999
ENTRYPOINT ["/usr/local/bin/docklane"]
