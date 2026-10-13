FROM golang:1-alpine3.24 AS builder

RUN apk add --no-cache git ca-certificates build-base su-exec olm-dev

COPY . /build
WORKDIR /build
RUN ./build.sh

FROM alpine:3.24

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache ffmpeg su-exec ca-certificates olm bash jq curl yq-go lottieconverter

# Fork: yt-dlp for TikTok link downloads (connector/forkextras.go)
ARG TARGETARCH
RUN curl -fsSL -o /usr/bin/yt-dlp "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_musllinux$([ "$TARGETARCH" = arm64 ] && echo _aarch64)" \
    && chmod +x /usr/bin/yt-dlp && /usr/bin/yt-dlp --version

COPY --from=builder /build/mautrix-whatsapp /usr/bin/mautrix-whatsapp
COPY --from=builder /build/docker-run.sh /docker-run.sh
VOLUME /data

CMD ["/docker-run.sh"]
