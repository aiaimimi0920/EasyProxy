# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM node:22-slim AS frontend
WORKDIR /src/frontend
ARG NPM_REGISTRY=https://registry.npmmirror.com
RUN npm config set registry ${NPM_REGISTRY}
COPY service/base/frontend/package.json service/base/frontend/package-lock.json ./
RUN npm ci
COPY service/base/frontend/ ./
RUN npm run build -- --outDir /tmp/frontend-dist

FROM --platform=$BUILDPLATFORM golang:1.24 AS easy-builder
ARG TARGETARCH
WORKDIR /src/easy-proxy
ARG GOPROXY=https://goproxy.cn,https://goproxy.io,direct
COPY service/base/go.mod service/base/go.sum ./
RUN go env -w GOPROXY=${GOPROXY} && go mod download
COPY service/base/ ./
COPY --from=frontend /tmp/frontend-dist/ ./internal/monitor/assets/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -tags "with_utls with_quic with_grpc with_wireguard with_gvisor" \
    -o /out/easy-proxy ./cmd/easy_proxies

FROM --platform=$BUILDPLATFORM golang:1.24 AS ech-builder
ARG TARGETARCH
WORKDIR /src/ech-workers
ARG GOPROXY=https://goproxy.cn,https://goproxy.io,direct
COPY upstreams/ech-workers/go.mod upstreams/ech-workers/go.sum ./
RUN go env -w GOPROXY=${GOPROXY} && go mod download
COPY upstreams/ech-workers/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o /out/ech-workers .

FROM --platform=$BUILDPLATFORM golang:1.24 AS cfst-builder
ARG TARGETARCH
ARG CFST_REPO=https://github.com/duquancai/CloudflareSpeedTest
ARG CFST_REF=77a6bbf63ff006330291a4f588bf80bcad8aea0c
ARG GOPROXY=https://goproxy.cn,https://goproxy.io,direct
WORKDIR /src/cfst
RUN git clone ${CFST_REPO} . \
    && git checkout ${CFST_REF}
RUN go env -w GOPROXY=${GOPROXY} && go mod download
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o /out/cfst .
RUN mkdir -p /out/share && cp ip.txt /out/share/ip.txt

FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS cloudflared-fetch
ARG TARGETARCH
ARG CLOUDFLARED_VERSION=2025.11.0
ARG CLOUDFLARED_AMD64_SHA256=17bb991c31fab3377977ef631e5701d5548181fe6cc9c2d51ae916ef758dea59
ARG CLOUDFLARED_ARM64_SHA256=3535f4686458918ccb1266b7840685b023768205574fa636d55a310fcb73135a
RUN sed -i 's|deb.debian.org|mirrors.aliyun.com|g' /etc/apt/sources.list.d/debian.sources \
    && apt-get -o Acquire::Retries=3 update \
    && apt-get install -y --no-install-recommends ca-certificates wget \
    && rm -rf /var/lib/apt/lists/*
RUN case "${TARGETARCH}" in \
        amd64) cloudflared_sha="${CLOUDFLARED_AMD64_SHA256}" ;; \
        arm64) cloudflared_sha="${CLOUDFLARED_ARM64_SHA256}" ;; \
        *) echo "unsupported cloudflared architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && wget -O /out-cloudflared "https://github.com/cloudflare/cloudflared/releases/download/${CLOUDFLARED_VERSION}/cloudflared-linux-${TARGETARCH}" \
    && echo "${cloudflared_sha}  /out-cloudflared" | sha256sum -c - \
    && chmod +x /out-cloudflared

FROM debian:bookworm-slim AS runtime
RUN sed -i 's|deb.debian.org|mirrors.aliyun.com|g' /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates gosu iproute2 nftables python3 python3-boto3 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -r -u 10001 easy \
    && mkdir -p /etc/easyproxy /etc/easyproxy/bootstrap /var/lib/easyproxy/config /var/lib/easyproxy/data /var/lib/easyproxy/connectors /usr/share/easyproxy/cfst \
    && chown -R easy:easy /etc/easyproxy /var/lib/easyproxy

WORKDIR /app
COPY --from=easy-builder /out/easy-proxy /usr/local/bin/easy-proxy
COPY --from=ech-builder /out/ech-workers /usr/local/bin/ech-workers
COPY --from=cfst-builder /out/cfst /usr/local/bin/cfst
COPY --from=cfst-builder /out/share/ip.txt /usr/share/easyproxy/cfst/ip.txt
COPY --from=cloudflared-fetch /out-cloudflared /usr/local/bin/cloudflared
COPY deploy/service/base/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY deploy/service/base/bootstrap-service-config.py /usr/local/bin/bootstrap-service-config.py
COPY scripts/easyproxy-import-code.py /usr/local/bin/easyproxy-import-code.py
RUN sed -i 's/\r$//' /usr/local/bin/docker-entrypoint.sh \
    && chmod +x /usr/local/bin/docker-entrypoint.sh
COPY --chown=easy:easy deploy/service/base/config.template.yaml /etc/easyproxy/config.yaml

EXPOSE 22323 29888 25000-25500
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["--config", "/var/lib/easyproxy/config/config.yaml"]
