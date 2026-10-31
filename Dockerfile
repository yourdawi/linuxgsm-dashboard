# LinuxGSM Web Dashboard - container image
#
# The dashboard runs in Docker mode in this image: game servers are containers of the official
# LinuxGSM images (gameservermanagers/gameserver), managed through the Docker socket.
#
#   docker build -t lgsm-dashboard .                       build from source (default)
#   docker build --target release -t lgsm-dashboard .      package dist/lgsm-dashboard-linux-<arch>
#
# The release target is used by the release workflow, so the image contains exactly the signed
# release binaries. The final stages have no RUN instructions, which lets buildx assemble every
# platform without emulation.

ARG GO_VERSION=1.27
ARG ALPINE_VERSION=3.22

# ---------------------------------------------------------------- build from source
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev-docker
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X main.Version=${VERSION}" -o /out/lgsm-dashboard .

# ---------------------------------------------------------------- common runtime base
FROM alpine:${ALPINE_VERSION} AS base
LABEL org.opencontainers.image.title="LinuxGSM Web Dashboard" \
      org.opencontainers.image.description="Web interface for LinuxGSM game servers running as Docker containers" \
      org.opencontainers.image.source="https://github.com/yourdawi/linuxgsm-dashboard" \
      org.opencontainers.image.licenses="MIT"

# Docker mode; server data lives in LGSM_DATA_DIR, which must be mounted at the same path as on
# the host (the dashboard hands it to the Docker engine as bind mount source).
ENV LGSM_RUNTIME=docker \
    LGSM_DATA_DIR=/srv/lgsm

# WORKDIR creates the directories without a RUN step.
WORKDIR /srv/lgsm
WORKDIR /config
VOLUME ["/config"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["wget", "-q", "--spider", "http://127.0.0.1:8080/"]

ENTRYPOINT ["/usr/local/bin/lgsm-dashboard", "-port", "8080", "-config-dir", "/config"]

# ---------------------------------------------------------------- release binaries
FROM base AS release
ARG TARGETARCH
COPY --chmod=0755 dist/lgsm-dashboard-linux-${TARGETARCH} /usr/local/bin/lgsm-dashboard

# ---------------------------------------------------------------- default: from source
FROM base AS source
COPY --from=build /out/lgsm-dashboard /usr/local/bin/lgsm-dashboard
