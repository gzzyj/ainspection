#!/usr/bin/env bash
set -euo pipefail

# ============================================================
# ainspection Profiling 基础设施状态检查
#
# 用法:
#   bash deploy/scripts/check.sh [--namespace=<ns>]
# ============================================================

NAMESPACE="ainspection"
PYROSCOPE_PORT="${PYROSCOPE_PORT:-4040}"

for arg in "$@"; do
  case "$arg" in
    --namespace=*)
      NAMESPACE="${arg#*=}"
      ;;
    --help|-h)
      echo "用法: bash deploy/scripts/check.sh [--namespace=<ns>]"
      echo ""
      echo "检查项:"
      echo "  1. Pyroscope 容器状态"
      echo "  2. Alloy DaemonSet 状态"
      echo "  3. Alloy Pod 日志 (最近 5 行)"
      echo "  4. Pyroscope /ready 端点"
      exit 0
      ;;
    *)
      echo "[ERROR] 未知参数: $arg"
      exit 1
      ;;
  esac
done

echo "============================================"
echo " ainspection Profiling 状态检查"
echo "============================================"
echo ""

PASS=0
FAIL=0

check_pass() {
  PASS=$((PASS + 1))
  echo "  [PASS] $1"
}

check_fail() {
  FAIL=$((FAIL + 1))
  echo "  [FAIL] $1"
}

# ------------------------------------------------------------------
# 1. Pyroscope 容器
# ------------------------------------------------------------------
echo "[1] Pyroscope 容器..."
if command -v docker &>/dev/null; then
  if docker ps --format '{{.Names}}' | grep -q '^pyroscope$'; then
    check_pass "Pyroscope 容器运行中"
    docker ps --filter "name=pyroscope" --format "       {{.ID}}  {{.Image}}  {{.Ports}}  {{.Status}}"
  elif docker ps -a --format '{{.Names}}' | grep -q '^pyroscope$'; then
    STATUS=$(docker inspect -f '{{.State.Status}}' pyroscope 2>/dev/null)
    check_fail "Pyroscope 容器状态: ${STATUS} (未运行)"
  else
    check_fail "Pyroscope 容器不存在"
  fi
else
  echo "  [SKIP] docker 不可用"
fi

# ------------------------------------------------------------------
# 2. Alloy DaemonSet
# ------------------------------------------------------------------
echo ""
echo "[2] Alloy DaemonSet..."
if kubectl -n "${NAMESPACE}" get daemonset alloy &>/dev/null; then
  DESIRED=$(kubectl -n "${NAMESPACE}" get daemonset alloy -o jsonpath='{.status.desiredNumberScheduled}')
  READY=$(kubectl -n "${NAMESPACE}" get daemonset alloy -o jsonpath='{.status.numberReady}')
  if [[ "$DESIRED" == "$READY" && "$READY" -gt 0 ]]; then
    check_pass "Alloy DaemonSet Ready: ${READY}/${DESIRED}"
  else
    check_fail "Alloy DaemonSet not ready: ${READY:-0}/${DESIRED:-0}"
  fi
  echo "       Pods:"
  kubectl -n "${NAMESPACE}" get pods -l app=alloy -o wide 2>/dev/null | sed 's/^/       /'
else
  check_fail "Alloy DaemonSet 不存在"
fi

# ------------------------------------------------------------------
# 3. Alloy Pod 日志
# ------------------------------------------------------------------
echo ""
echo "[3] Alloy 最近日志..."
ALLOY_POD=$(kubectl -n "${NAMESPACE}" get pods -l app=alloy -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [[ -n "$ALLOY_POD" ]]; then
  echo "       Pod: ${ALLOY_POD}"
  kubectl -n "${NAMESPACE}" logs "${ALLOY_POD}" --tail=5 2>/dev/null | sed 's/^/       /' || echo "       (无法获取日志)"
else
  check_fail "无运行中的 Alloy Pod"
fi

# ------------------------------------------------------------------
# 4. Pyroscope /ready
# ------------------------------------------------------------------
echo ""
echo "[4] Pyroscope /ready 端点..."
if command -v curl &>/dev/null; then
  PYROSCOPE_HOST="${PYROSCOPE_HOST:-localhost}"
  if curl -sf "http://${PYROSCOPE_HOST}:${PYROSCOPE_PORT}/ready" >/dev/null 2>&1; then
    check_pass "Pyroscope /ready 返回 OK"
  else
    check_fail "Pyroscope /ready 不可达 (http://${PYROSCOPE_HOST}:${PYROSCOPE_PORT}/ready)"
  fi
else
  echo "  [SKIP] curl 不可用"
fi

# ------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------
echo ""
echo "============================================"
echo " 结果: ${PASS} 通过, ${FAIL} 失败"
echo "============================================"

if [[ "$FAIL" -gt 0 ]]; then
  exit 1
fi
