# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine3.24 AS go-build
WORKDIR /src
COPY go.mod ./
COPY cmd/orchestrator/ ./cmd/orchestrator/
COPY internal/ ./internal/
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.Commit=${COMMIT}" \
    -o /orchestrator ./cmd/orchestrator

FROM node:26-bookworm-slim AS poster-build
# Keep binutils and native debug symbols out of the runtime image.
RUN apt-get update && \
    apt-get install -y --no-install-recommends binutils && \
    strip --strip-unneeded /usr/local/bin/node && \
    rm -rf /var/lib/apt/lists/*

FROM ubuntu:26.04
LABEL org.opencontainers.image.source="https://github.com/oszuidwest/zw-xposter"
LABEL org.opencontainers.image.description="Feed orchestrator and HTTP poster for ZuidWest"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=poster-build /usr/local/bin/node /usr/local/bin/node
# The release timestamp invalidates package-update layers on each release build.
ARG BUILD_TIME=unknown
ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get upgrade -y && \
    apt-get install -y --no-install-recommends ca-certificates libatomic1 libstdc++6 tini util-linux tzdata && \
    rm -rf /var/lib/apt/lists/* && \
    install -d -o 1000 -g 1000 -m 0700 /data

ENV NODE_ENV=production \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    TZ=Europe/Amsterdam \
    HOME=/home/ubuntu \
    DATA_DIR=/data

WORKDIR /app/poster
COPY --from=go-build --chmod=0555 /orchestrator /app/orchestrator
COPY poster/server.mjs poster/x-client.mjs poster/publication.mjs poster/timeline.mjs poster/health.mjs poster/media.mjs poster/subtitles.mjs ./
COPY --chmod=0555 container/entrypoint.sh /app/entrypoint.sh
COPY container/healthcheck.mjs /app/healthcheck.mjs

USER 1000:1000
HEALTHCHECK --interval=30s --timeout=10s --start-period=90s --retries=3 \
    CMD ["node", "/app/healthcheck.mjs"]
ENTRYPOINT ["/usr/bin/tini", "--", "/app/entrypoint.sh"]
CMD ["serve"]
