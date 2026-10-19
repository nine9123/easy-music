# syntax=docker/dockerfile:1

# Standard library only, so there's nothing to download: no go.sum, no tidy.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/easy-music .

# Not scratch (unlike idlegate): URL imports need yt-dlp (Python) and ffmpeg.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates ffmpeg python3 tzdata

# yt-dlp has to keep up with YouTube, so it comes from its own releases rather
# than the (slower) distro package. Rebuild the image to update it, or pin a
# release with --build-arg YTDLP_VERSION=2025.10.22. The checksum file comes
# from the same release: it catches a corrupt download, not a hostile one.
ARG YTDLP_VERSION=latest
RUN set -eu; \
    if [ "$YTDLP_VERSION" = latest ]; then base=https://github.com/yt-dlp/yt-dlp/releases/latest/download; \
    else base="https://github.com/yt-dlp/yt-dlp/releases/download/$YTDLP_VERSION"; fi; \
    wget -qO /usr/local/bin/yt-dlp "$base/yt-dlp"; \
    wget -qO /tmp/SHA2-256SUMS "$base/SHA2-256SUMS"; \
    cd /usr/local/bin && grep ' yt-dlp$' /tmp/SHA2-256SUMS | sha256sum -c -; \
    chmod 0755 /usr/local/bin/yt-dlp; rm /tmp/SHA2-256SUMS; \
    yt-dlp --version

COPY --from=build /out/easy-music /usr/local/bin/easy-music

ENV MUSIC_DIRS=/music \
    DOWNLOAD_DIR=/downloads \
    CACHE_DIR=/cache \
    STATE_DIR=/config \
    LISTEN=:8080
EXPOSE 8080
HEALTHCHECK --interval=1m --timeout=5s CMD ["easy-music", "healthcheck"]
ENTRYPOINT ["easy-music"]
