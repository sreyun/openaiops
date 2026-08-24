# AIOps marketing website (static + SQLite analytics API)
# 默认走国内基础镜像仓（与主仓生产 Dockerfile 一致）；海外可覆盖：
#   docker build --build-arg BASE_REGISTRY=docker.io ...
ARG BASE_REGISTRY=swr.cn-east-3.myhuaweicloud.com/sreyun/docker.io

FROM ${BASE_REGISTRY}/alpine:3.20
WORKDIR /app

# Alpine 软件源切国内镜像，加速 apk
RUN set -eux; \
    if [ -f /etc/apk/repositories ]; then \
      sed -i \
        -e 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' \
        -e 's|nl.alpinelinux.org|mirrors.aliyun.com|g' \
        /etc/apk/repositories; \
    fi; \
    apk add --no-cache python3 ca-certificates tzdata; \
    ln -sf python3 /usr/bin/python; \
    addgroup -S -g 10001 web; \
    adduser -S -u 10001 -G web -H -s /sbin/nologin web; \
    mkdir -p /app/data; \
    chown -R web:web /app

COPY --chown=web:web . /app

USER web
ENV WEBSITE_DB=/app/data/website.db \
    WEBSITE_ADMIN_PASSWORD=aiops2026 \
    PYTHONUNBUFFERED=1 \
    TZ=Asia/Shanghai
EXPOSE 8090
VOLUME ["/app/data"]
CMD ["python", "serve.py", "8090"]
