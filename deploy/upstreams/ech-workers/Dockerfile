# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.24 AS builder
ARG TARGETARCH
ARG GOPROXY=https://goproxy.cn,https://goproxy.io,direct
WORKDIR /src/ech-workers

COPY upstreams/ech-workers/go.mod upstreams/ech-workers/go.sum ./
RUN go env -w GOPROXY=${GOPROXY} && go mod download
COPY upstreams/ech-workers/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o /out/ech-workers .

FROM debian:bookworm-slim AS runtime
RUN sed -i 's|deb.debian.org|mirrors.aliyun.com|g' /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/ech-workers /usr/local/bin/ech-workers

EXPOSE 30000
ENTRYPOINT ["/usr/local/bin/ech-workers"]
CMD ["-h"]
