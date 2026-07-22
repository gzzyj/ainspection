#!/usr/bin/env bash
set -euo pipefail

# ============================================================
# ainspection Profiling 基础设施一键清理
#
# 用法:
#   bash deploy/cleanup.sh [--namespace=<ns>] [--dry-run]
#
# 选项:
#   --namespace=<ns>   Kubernetes namespace (默认: ainspection)
#   --dry-run          仅显示将执行的操作, 不实际执行
#   --keep-pyroscope   保留 Pyroscope 容器
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

NAMESPACE="ainspection"
DRY_RUN=false
KEEP_PYROSCOPE=false

for arg in "$@"; do
  case "$arg" in
    --namespace=*)
      NAMESPACE="${arg#*=}"
      ;;
    --dry-run)
      DRY_RUN=true
      ;;
    --keep-pyroscope)
      KEEP_PYROSCOPE=true
      ;;
    --help|-h)
      echo "用法: bash deploy/cleanup.sh [--namespace=<ns>] [--dry-run] [--keep-pyroscope]"
      echo ""
      echo "选项:"
      echo "  --namespace=<ns>   Kubernetes namespace (默认: ainspection)"
      echo "  --dry-run          仅显示将执行的操作"
      echo "  --keep-pyroscope   保留 Pyroscope 容器"
      exit 0
      ;;
    *)
      echo "[ERROR] 未知参数: $arg"
      exit 1
      ;;
  esac
done

KUBECTL="kubectl"
if $DRY_RUN; then
  KUBECTL="kubectl --dry-run=client"
fi

echo "============================================"
echo " ainspection Profiling 基础设施清理"
echo "============================================"
echo " Namespace:       ${NAMESPACE}"
echo " Dry Run:         ${DRY_RUN}"
echo " Keep Pyroscope:  ${KEEP_PYROSCOPE}"
echo "============================================"
echo ""

# ------------------------------------------------------------------
# Step 1: 删除 Alloy DaemonSet
# ------------------------------------------------------------------
echo "[1/4] 删除 Alloy DaemonSet..."
if kubectl -n "${NAMESPACE}" get daemonset alloy &>/dev/null; then
  if $DRY_RUN; then
    echo "  [DRY-RUN] kubectl delete daemonset alloy -n ${NAMESPACE}"
  else
    kubectl delete daemonset alloy -n "${NAMESPACE}" --ignore-not-found
    echo "  [OK] DaemonSet 已删除"
  fi
else
  echo "  [INFO] DaemonSet 不存在, 跳过"
fi

# ------------------------------------------------------------------
# Step 2: 删除 ConfigMap
# ------------------------------------------------------------------
echo "[2/4] 删除 Alloy ConfigMap..."
if kubectl -n "${NAMESPACE}" get configmap alloy-config &>/dev/null; then
  if $DRY_RUN; then
    echo "  [DRY-RUN] kubectl delete configmap alloy-config -n ${NAMESPACE}"
  else
    kubectl delete configmap alloy-config -n "${NAMESPACE}" --ignore-not-found
    echo "  [OK] ConfigMap 已删除"
  fi
else
  echo "  [INFO] ConfigMap 不存在, 跳过"
fi

# ------------------------------------------------------------------
# Step 3: 删除 RBAC
# ------------------------------------------------------------------
echo "[3/4] 删除 Alloy RBAC..."

delete_rbac() {
  local resource="$1"
  local name="$2"
  local namespace="${3:-}"
  if [[ -n "$namespace" ]]; then
    if kubectl -n "$namespace" get "$resource" "$name" &>/dev/null; then
      if $DRY_RUN; then
        echo "  [DRY-RUN] kubectl delete ${resource} ${name} -n ${namespace}"
      else
        kubectl delete "$resource" "$name" -n "$namespace" --ignore-not-found
        echo "  [OK] ${resource}/${name} 已删除"
      fi
    fi
  else
    if kubectl get "$resource" "$name" &>/dev/null; then
      if $DRY_RUN; then
        echo "  [DRY-RUN] kubectl delete ${resource} ${name}"
      else
        kubectl delete "$resource" "$name" --ignore-not-found
        echo "  [OK] ${resource}/${name} 已删除"
      fi
    fi
  fi
}

delete_rbac "serviceaccount" "alloy" "${NAMESPACE}"
delete_rbac "clusterrolebinding" "alloy"
delete_rbac "clusterrole" "alloy"

# ------------------------------------------------------------------
# Step 4: 停止 Pyroscope
# ------------------------------------------------------------------
echo "[4/4] 停止 Pyroscope..."
if ! $KEEP_PYROSCOPE; then
  if command -v docker &>/dev/null; then
    CONTAINER_NAME="${CONTAINER_NAME:-pyroscope}"
    if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
      if $DRY_RUN; then
        echo "  [DRY-RUN] docker stop ${CONTAINER_NAME} && docker rm ${CONTAINER_NAME}"
      else
        docker stop "${CONTAINER_NAME}" >/dev/null 2>&1 || true
        docker rm "${CONTAINER_NAME}" >/dev/null 2>&1 || true
        echo "  [OK] Pyroscope 容器已停止并移除"
      fi
    else
      echo "  [INFO] Pyroscope 容器不存在, 跳过"
    fi
  else
    echo "  [WARN] docker 不可用, 无法管理 Pyroscope 容器"
  fi
else
  echo "  [SKIP] 保留 Pyroscope 容器"
fi

# ------------------------------------------------------------------
# Step 5 (可选): 删除 Namespace
# ------------------------------------------------------------------
echo ""
echo "============================================"
echo " 清理完成"
echo "============================================"
echo " 提示: 如需删除 namespace, 请手动执行:"
echo "   kubectl delete namespace ${NAMESPACE}"
echo "============================================"
