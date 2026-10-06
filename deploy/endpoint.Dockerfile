# syntax=docker/dockerfile:1
FROM debian:bookworm-slim AS release
ARG TARGETARCH
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl tar \
    && rm -rf /var/lib/apt/lists/*
# Release hashes are supplied by the official GitHub release metadata:
# https://api.github.com/repos/TrustTunnel/TrustTunnel/releases/tags/v1.1.0
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) arch=x86_64; digest=91c2ea3db7416a01b5258a4c047ec22890490bc55e1b194206031aa75144f0e7 ;; \
      arm64) arch=aarch64; digest=c2aee17a1ced349283cba4775202e2baba053b8ea835d4cc23dc67d16c6b9686 ;; \
      *) echo "Unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl --fail --show-error --location --retry 3 \
      "https://github.com/TrustTunnel/TrustTunnel/releases/download/v1.1.0/trusttunnel-v1.1.0-linux-${arch}.tar.gz" \
      -o /tmp/trusttunnel.tar.gz; \
    echo "$digest  /tmp/trusttunnel.tar.gz" | sha256sum -c -; \
    mkdir -p /release; \
    tar -xzf /tmp/trusttunnel.tar.gz --strip-components=1 -C /release

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=release /release/trusttunnel_endpoint /usr/local/bin/trusttunnel_endpoint
COPY --from=release /release/LICENSE /usr/share/doc/trusttunnel/LICENSE
COPY --chmod=755 dist/endpoint-agent /usr/local/bin/endpoint-agent
WORKDIR /data/config
ENTRYPOINT ["/usr/local/bin/endpoint-agent"]
