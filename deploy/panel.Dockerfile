# syntax=docker/dockerfile:1
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --chmod=755 dist/panel /usr/local/bin/panel
ENTRYPOINT ["/usr/local/bin/panel"]
