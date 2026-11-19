# syntax=docker/dockerfile:1
#
# Container image of the watchmydog service in the cloud role (ADR-0011).
# The edge (cameras, go2rtc, ffmpeg, the Mac's loudspeaker) does not run in a
# container: it needs the Mac's devices and macOS permissions.
#
#   docker build -t watchmydog .
#   docker buildx build --platform linux/amd64,linux/arm64 -t watchmydog .
#
# Both build stages run on the machine that builds (BUILDPLATFORM); Go
# cross-compiles for the target, so a multi-platform build needs no emulation.

# --- web client -------------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:24.11.1-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# --- service ----------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.25.6-alpine AS service
ARG TARGETOS TARGETARCH
ARG VERSION=dev COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
# CGO off: the SQLite driver is pure Go, so the binary is fully static.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/maikomr/watchmydog/internal/buildinfo.Version=$VERSION -X github.com/maikomr/watchmydog/internal/buildinfo.Commit=$COMMIT" \
      -o /out/watchmydog ./cmd/watchmydog
# An empty data directory, so the runtime image can hand it to its user.
RUN mkdir -p /out/data

# --- runtime ----------------------------------------------------------------
# distroless/static: no shell, no package manager, runs as an unprivileged
# user (uid 65532).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=service /out/watchmydog /app/watchmydog
COPY --from=web /src/web/dist /app/web
COPY deploy/watchmydog.cloud.yaml /app/watchmydog.yaml
# Owned by the unprivileged user; a new named volume inherits that.
COPY --from=service --chown=65532:65532 /out/data /data
# Database and VAPID keys. Mount a volume here to keep them.
VOLUME ["/data"]
EXPOSE 8080
ENV WATCHMYDOG_CONFIG=/app/watchmydog.yaml
# The login secret comes from the environment: -e WATCHMYDOG_SECRET=...
ENTRYPOINT ["/app/watchmydog"]
