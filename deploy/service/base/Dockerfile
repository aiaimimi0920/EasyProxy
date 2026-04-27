# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM node:22-slim AS frontend
WORKDIR /src/frontend
ARG NPM_REGISTRY=https://registry.npmmirror.com
RUN npm config set registry ${NPM_REGISTRY}
COPY service/base/frontend/package.json service/base/frontend/package-lock.json ./
RUN npm ci
COPY service/base/frontend/ ./
RUN npx vite build --outDir /tmp/frontend-dist

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
ARG CLOUDFLARED_VERSION=2025.11.0
ARG CLOUDFLARED_DOWNLOAD_URL=https://github.com/cloudflare/cloudflared/releases/download/2025.11.0/cloudflared-linux-amd64
ARG CLOUDFLARED_SHA256=17bb991c31fab3377977ef631e5701d5548181fe6cc9c2d51ae916ef758dea59
RUN sed -i 's|deb.debian.org|mirrors.aliyun.com|g' /etc/apt/sources.list.d/debian.sources \
    && apt-get -o Acquire::Retries=3 update \
    && apt-get install -y --no-install-recommends ca-certificates wget \
    && rm -rf /var/lib/apt/lists/*
RUN wget -O /out-cloudflared ${CLOUDFLARED_DOWNLOAD_URL} \
    && echo "${CLOUDFLARED_SHA256}  /out-cloudflared" | sha256sum -c - \
    && chmod +x /out-cloudflared

FROM debian:bookworm-slim AS runtime
RUN sed -i 's|deb.debian.org|mirrors.aliyun.com|g' /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates gosu python3 python3-boto3 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -r -u 10001 easy \
    && mkdir -p /etc/easy-proxy /etc/easy-proxy/bootstrap /var/lib/easy-proxy/data /var/lib/easy-proxy/connectors /usr/local/share/cfst \
    && chown -R easy:easy /etc/easy-proxy /var/lib/easy-proxy

WORKDIR /app
COPY --from=easy-builder /out/easy-proxy /usr/local/bin/easy-proxy
COPY --from=ech-builder /out/ech-workers /usr/local/bin/ech-workers
COPY --from=cfst-builder /out/cfst /usr/local/bin/cfst
COPY --from=cfst-builder /out/share/ip.txt /usr/local/share/cfst/ip.txt
COPY --from=cloudflared-fetch /out-cloudflared /usr/local/bin/cloudflared
COPY deploy/service/base/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY deploy/service/base/bootstrap-service-config.py /usr/local/bin/bootstrap-service-config.py
COPY scripts/easyproxy-import-code.py /usr/local/bin/easyproxy-import-code.py
RUN chmod +x /usr/local/bin/docker-entrypoint.sh
COPY --chown=easy:easy deploy/service/base/config.template.yaml /etc/easy-proxy/config.yaml

EXPOSE 22323 29888 25000-25500
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["--config", "/etc/easy-proxy/config.yaml"]
