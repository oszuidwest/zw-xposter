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
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
# Keep binutils and native debug symbols out of the runtime image.
RUN apt-get update && \
    apt-get install -y --no-install-recommends binutils && \
    strip --strip-unneeded /usr/local/bin/node && \
    rm -rf /var/lib/apt/lists/*
WORKDIR /build
COPY poster/package.json poster/package-lock.json ./
RUN npm ci --omit=dev && \
    node node_modules/playwright-core/cli.js install --no-shell chromium

FROM ubuntu:26.04
LABEL org.opencontainers.image.source="https://github.com/oszuidwest/zw-xposter"
LABEL org.opencontainers.image.description="Feed orchestrator and browser poster for ZuidWest"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=poster-build /usr/local/bin/node /usr/local/bin/node
# The release timestamp invalidates package-update layers on each release build.
ARG BUILD_TIME=unknown
ARG DEBIAN_FRONTEND=noninteractive
# Use Playwright's dependency list for this Ubuntu version without copying npm
# or the build image into the runtime. Keep the desktop fonts and Mesa renderer.
RUN --mount=type=bind,from=poster-build,source=/build/node_modules,target=/build/node_modules \
    apt-get update && apt-get upgrade -y && \
    apt-get install -y --no-install-recommends ca-certificates libatomic1 libstdc++6 tini util-linux tzdata xvfb \
        fonts-dejavu-core fonts-ubuntu fonts-croscore libgl1-mesa-dri libegl1 libgl1 && \
    node /build/node_modules/playwright-core/cli.js install-deps chromium && \
    rm -rf /var/lib/apt/lists/* && \
    install -d -o 1000 -g 1000 -m 0700 /data && \
    install -d -m 1777 /tmp/.X11-unix

ENV NODE_ENV=production \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    TZ=Europe/Amsterdam \
    HOME=/home/ubuntu \
    PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    DATA_DIR=/data

WORKDIR /app/poster
COPY --from=poster-build /ms-playwright /ms-playwright
COPY --from=poster-build /build/node_modules ./node_modules
COPY --from=go-build --chmod=0555 /orchestrator /app/orchestrator
COPY poster/server.mjs poster/timeline.mjs poster/health.mjs poster/debug.mjs poster/media.mjs poster/x-ui.json ./
COPY --chmod=0555 container/entrypoint.sh /app/entrypoint.sh
COPY container/healthcheck.mjs /app/healthcheck.mjs

USER 1000:1000
HEALTHCHECK --interval=30s --timeout=10s --start-period=90s --retries=3 \
    CMD ["node", "/app/healthcheck.mjs"]
ENTRYPOINT ["/usr/bin/tini", "--", "/app/entrypoint.sh"]
CMD ["serve"]
