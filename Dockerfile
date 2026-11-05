# lanbus hub —— 零依赖，只需标准库
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    LANBUS_DATA_DIR=/data

WORKDIR /app

# 先只拷打包元数据与源码，利用层缓存
COPY pyproject.toml README.md LICENSE ./
COPY src/ ./src/

RUN pip install --no-cache-dir . && \
    useradd --create-home --uid 10001 lanbus && \
    mkdir -p /data && chown -R lanbus:lanbus /data

USER lanbus
VOLUME ["/data"]
EXPOSE 8788

# 镜像里没有 curl，用标准库探活
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD python -c "import urllib.request;urllib.request.urlopen('http://127.0.0.1:8788/health',timeout=3)"

ENTRYPOINT ["lanbus-hub"]
CMD ["--bind", "0.0.0.0", "--port", "8788"]