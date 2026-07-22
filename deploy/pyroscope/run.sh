#!/usr/bin/env bash
set -euo pipefail

# ============================================================
# Pyroscope Server 启动脚本
# 用法: bash deploy/pyroscope/run.sh
# 默认监听 0.0.0.0:4040
# ============================================================

CONTAINER_NAME="${CONTAINER_NAME:-pyroscope}"
IMAGE="${IMAGE:-grafana/pyroscope:1.8.2}"
HOST_PORT="${HOST_PORT:-4040}"

# 检查 Docker 是否可用
if ! command -v docker &>/dev/null; then
  echo "[ERROR] docker 未安装或不在 PATH 中"
  exit 1
fi

# 如果同名容器已存在，先检查其状态
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
  STATUS=$(docker inspect -f '{{.State.Status}}' "${CONTAINER_NAME}" 2>/dev/null || echo "unknown")
  case "${STATUS}" in
    running)
      echo "[INFO] Pyroscope 容器已在运行 (name=${CONTAINER_NAME})"
      docker ps --filter "name=${CONTAINER_NAME}" --format "  {{.ID}}  {{.Image}}  {{.Ports}}  {{.Status}}"
      echo "[INFO] 访问地址: http://localhost:${HOST_PORT}"
      exit 0
      ;;
    exited|created)
      echo "[INFO] 发现已停止的容器 ${CONTAINER_NAME}，正在移除..."
      docker rm -f "${CONTAINER_NAME}" >/dev/null
      ;;
    *)
      echo "[INFO] 移除旧容器 ${CONTAINER_NAME} (状态: ${STATUS})"
      docker rm -f "${CONTAINER_NAME}" >/dev/null 2>/dev/null || true
      ;;
  esac
fi

echo "[INFO] 启动 Pyroscope Server..."
docker run -d \
  --name "${CONTAINER_NAME}" \
  -p "${HOST_PORT}:4040" \
  --restart unless-stopped \
  "${IMAGE}"

# 等待服务就绪
echo "[INFO] 等待 Pyroscope 就绪..."
for i in $(seq 1 30); do
  if curl -sf http://localhost:${HOST_PORT}/ready >/dev/null 2>&1; then
    echo "[INFO] Pyroscope 已就绪: http://localhost:${HOST_PORT}"
    docker ps --filter "name=${CONTAINER_NAME}" --format "  {{.ID}}  {{.Image}}  {{.Ports}}  {{.Status}}"
    exit 0
  fi
  sleep 1
done

echo "[WARN] Pyroscope 启动超时，请手动检查: docker logs ${CONTAINER_NAME}"
exit 1
