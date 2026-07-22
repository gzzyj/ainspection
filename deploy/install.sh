#!/usr/bin/env bash
set -uo pipefail

# ============================================================
# ainspection Profiling 基础设施一键部署
#
# 用法:
#   bash deploy/install.sh --user=<name> --pyroscope-endpoint=<ip:port>
#
# 选项:
#   --user=<name>              开发者标识 (必填, 用于隔离链路)
#   --pyroscope-endpoint=<url> Pyroscope 地址 (必填, 如 http://10.106.19.42:4040)
#   --namespace=<ns>           Kubernetes namespace (默认: ainspection)
#   --dry-run                  仅验证, 不执行实际操作
#   --skip-pyroscope           跳过 Pyroscope 启动 (已手动启动时使用)
#
# 失败回退:
#   任一步骤失败后, 自动回退已部署资源 (RBAC / ConfigMap / DaemonSet),
#   ConfigMap 存在时优先恢复备份, 确保环境恢复到部署前状态.
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# 默认值
NAMESPACE="ainspection"
DRY_RUN=false
SKIP_PYROSCOPE=false
USER_NAME=""
PYROSCOPE_ENDPOINT=""

# 解析参数
for arg in "$@"; do
  case "$arg" in
    --user=*)
      USER_NAME="${arg#*=}"
      ;;
    --pyroscope-endpoint=*)
      PYROSCOPE_ENDPOINT="${arg#*=}"
      ;;
    --namespace=*)
      NAMESPACE="${arg#*=}"
      ;;
    --dry-run)
      DRY_RUN=true
      ;;
    --skip-pyroscope)
      SKIP_PYROSCOPE=true
      ;;
    --help|-h)
      echo "用法: bash deploy/install.sh --user=<name> --pyroscope-endpoint=<url>"
      echo ""
      echo "选项:"
      echo "  --user=<name>              开发者标识 (必填)"
      echo "  --pyroscope-endpoint=<url> Pyroscope 地址 (必填)"
      echo "  --namespace=<ns>           Kubernetes namespace (默认: ainspection)"
      echo "  --dry-run                  仅验证, 不执行实际操作"
      echo "  --skip-pyroscope           跳过 Pyroscope 启动"
      exit 0
      ;;
    *)
      echo "[ERROR] 未知参数: $arg"
      exit 1
      ;;
  esac
done

# 必填参数检查
if [[ -z "$USER_NAME" ]]; then
  echo "[ERROR] --user=<name> 为必填参数"
  exit 1
fi
if [[ -z "$PYROSCOPE_ENDPOINT" ]]; then
  echo "[ERROR] --pyroscope-endpoint=<url> 为必填参数"
  exit 1
fi

# 验证 user 名称 (字母数字下划线连字符)
if ! echo "$USER_NAME" | grep -qE '^[a-zA-Z0-9_-]+$'; then
  echo "[ERROR] --user 名称只能包含字母、数字、下划线、连字符"
  exit 1
fi

echo "============================================"
echo " ainspection Profiling 基础设施部署"
echo "============================================"
echo " 用户:        ${USER_NAME}"
echo " Pyroscope:   ${PYROSCOPE_ENDPOINT}"
echo " Namespace:   ${NAMESPACE}"
echo " Dry Run:     ${DRY_RUN}"
echo "============================================"
echo ""

# 预检
if ! command -v kubectl &>/dev/null; then
  echo "[ERROR] kubectl 未安装或不在 PATH 中"
  exit 1
fi
if ! $SKIP_PYROSCOPE && ! command -v docker &>/dev/null; then
  echo "[ERROR] docker 未安装或不在 PATH 中"
  exit 1
fi

# ------------------------------------------------------------------
# 回退基础设施
# ------------------------------------------------------------------

# 步骤位标记 (按位记录已完成的步骤)
#   0x01 — RBAC applied
#   0x02 — ConfigMap applied
#   0x04 — DaemonSet applied
STEP_DONE=0

# ConfigMap 备份文件路径
CM_BACKUP=""

# Pyroscope 是否由本次部署启动
PYROSCOPE_STARTED=false

# 临时目录 (延迟创建)
TMPDIR=""

rollback_handler() {
  local exit_code=$?

  # 清理临时目录 (无论成功失败)
  if [[ -n "${TMPDIR}" && -d "${TMPDIR}" ]]; then
    rm -rf "${TMPDIR}"
  fi

  # 成功退出, 不做回退
  if [[ $exit_code -eq 0 ]]; then
    return 0
  fi

  echo ""
  echo "============================================"
  echo " [ROLLBACK] 部署失败 (exit=${exit_code})，执行回退..."
  echo "============================================"
  echo " STEP_DONE=0x$(printf '%x' ${STEP_DONE})"
  echo ""

  # 反向回退: DaemonSet → ConfigMap → RBAC → Pyroscope

  # 0x04 — DaemonSet
  if [[ $((STEP_DONE & 0x04)) -ne 0 ]]; then
    echo "  [ROLLBACK] 删除 DaemonSet..."
    kubectl delete daemonset alloy -n "${NAMESPACE}" --ignore-not-found 2>/dev/null || true
  fi

  # 0x02 — ConfigMap (优先恢复备份)
  if [[ $((STEP_DONE & 0x02)) -ne 0 ]]; then
    if [[ -n "${CM_BACKUP}" && -f "${CM_BACKUP}" ]]; then
      echo "  [ROLLBACK] 恢复 ConfigMap 备份..."
      kubectl apply -f "${CM_BACKUP}" 2>/dev/null || true
    else
      echo "  [ROLLBACK] 删除 ConfigMap (首次部署)..."
      kubectl delete configmap alloy-config -n "${NAMESPACE}" --ignore-not-found 2>/dev/null || true
    fi
  fi

  # 0x01 — RBAC
  if [[ $((STEP_DONE & 0x01)) -ne 0 ]]; then
    echo "  [ROLLBACK] 删除 RBAC..."
    sed "s/__NAMESPACE__/${NAMESPACE}/g" "${PROJECT_DIR}/deploy/alloy/rbac.yaml" | \
      kubectl delete -f - --ignore-not-found 2>/dev/null || true
  fi

  # Pyroscope (由本次部署启动才清理)
  if $PYROSCOPE_STARTED; then
    echo "  [ROLLBACK] 停止 Pyroscope 容器..."
    docker stop pyroscope 2>/dev/null || true
    docker rm pyroscope 2>/dev/null || true
  fi

  echo ""
  echo "============================================"
  echo " [ROLLBACK] 回退完成，环境已恢复"
  echo "============================================"
}

# 注册 EXIT trap (在部署步骤开始前)
trap 'rollback_handler' EXIT

# ------------------------------------------------------------------
# Helper: 生成用户 River 配置块
# ------------------------------------------------------------------
generate_user_river_blocks() {
  local user="$1"
  local endpoint="$2"
  local types="cpu heap goroutine mutex block"
  local paths
  declare -A paths=(
    ["cpu"]="/debug/pprof/profile?seconds=15"
    ["heap"]="/debug/pprof/heap"
    ["goroutine"]="/debug/pprof/goroutine"
    ["mutex"]="/debug/pprof/mutex"
    ["block"]="/debug/pprof/block"
  )

  cat <<RIVER

    // === Pipeline: ${user} ===

    discovery.relabel "ainspection_${user}_targets" {
      targets = discovery.kubernetes.ainspection_pods.targets

      rule {
        source_labels = ["__meta_kubernetes_pod_label_app"]
        action = "keep"
        regex = ".+"
      }
      rule {
        source_labels = ["__meta_kubernetes_pod_phase"]
        action = "keep"
        regex = "Running"
      }
    }
RIVER

  for ptype in $types; do
    local path="${paths[$ptype]}"
    cat <<RIVER

    discovery.relabel "ainspection_${user}_${ptype}" {
      targets = discovery.relabel.ainspection_${user}_targets.output

      rule {
        source_labels = ["__meta_kubernetes_pod_ip"]
        target_label = "__address__"
        replacement = "\$1:80"
      }
      rule {
        target_label = "__profile_path__"
        replacement = "${path}"
      }
      rule {
        source_labels = ["__meta_kubernetes_pod_label_app"]
        target_label = "service_name"
      }
      rule {
        target_label = "profile_type"
        replacement = "${ptype}"
      }
      rule {
        target_label = "user"
        replacement = "${user}"
      }
    }

    pyroscope.scrape "ainspection_${user}_${ptype}" {
      targets    = discovery.relabel.ainspection_${user}_${ptype}.output
      forward_to = [pyroscope.write.ainspection_sink_${user}.receiver]
      scrape_interval = "60s"
      profiling_config {
        path_prefix = ""
      }
    }
RIVER
  done

  cat <<RIVER

    pyroscope.write "ainspection_sink_${user}" {
      endpoint {
        url = "${endpoint}"
      }
      external_labels = {
        "user" = "${user}",
      }
    }
RIVER
}

# ------------------------------------------------------------------
# Helper: 读取现有 ConfigMap 中的用户列表
# ------------------------------------------------------------------
get_existing_users() {
  if kubectl get configmap alloy-config -n "${NAMESPACE}" &>/dev/null; then
    kubectl get configmap alloy-config -n "${NAMESPACE}" \
      -o jsonpath='{.metadata.annotations.ainspection\.io/users}' 2>/dev/null || echo ""
  else
    echo ""
  fi
}

# ------------------------------------------------------------------
# Step 1: 创建 Namespace
# ------------------------------------------------------------------
echo "[1/6] 创建 Namespace..."
if kubectl get namespace "${NAMESPACE}" &>/dev/null; then
  echo "  [INFO] Namespace ${NAMESPACE} 已存在"
else
  if $DRY_RUN; then
    echo "  [DRY-RUN] kubectl create namespace ${NAMESPACE}"
  else
    if ! kubectl create namespace "${NAMESPACE}"; then
      echo "  [ERROR] 创建 Namespace 失败"
      exit 1
    fi
    echo "  [OK] Namespace ${NAMESPACE} 已创建"
  fi
fi

# ------------------------------------------------------------------
# Step 2: 启动 Pyroscope
# ------------------------------------------------------------------
echo "[2/6] 启动 Pyroscope Server..."
if $SKIP_PYROSCOPE; then
  echo "  [SKIP] 跳过 Pyroscope 启动"
else
  if $DRY_RUN; then
    echo "  [DRY-RUN] bash ${PROJECT_DIR}/deploy/pyroscope/run.sh"
  else
    # 记录 Pyroscope 是否已运行 (用于判断是否需要回退清理)
    pyroscope_was_running=false
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^pyroscope$'; then
      pyroscope_was_running=true
    fi

    if ! bash "${PROJECT_DIR}/deploy/pyroscope/run.sh"; then
      echo "  [ERROR] Pyroscope 启动失败"
      exit 1
    fi

    # 仅在本次部署真正启动了容器时标记 (已运行的不算)
    if ! $pyroscope_was_running; then
      PYROSCOPE_STARTED=true
    fi
  fi
fi

# ------------------------------------------------------------------
# Step 3: 部署 RBAC
# ------------------------------------------------------------------
echo "[3/6] 部署 Alloy RBAC..."
if $DRY_RUN; then
  sed "s/__NAMESPACE__/${NAMESPACE}/g" "${PROJECT_DIR}/deploy/alloy/rbac.yaml" | \
    kubectl apply --dry-run=client -f -
else
  sed "s/__NAMESPACE__/${NAMESPACE}/g" "${PROJECT_DIR}/deploy/alloy/rbac.yaml" | \
    kubectl apply -f -
  if [[ ${PIPESTATUS[0]} -ne 0 ]]; then
    echo "  [ERROR] RBAC 部署失败"
    exit 1
  fi
fi
STEP_DONE=$((STEP_DONE | 0x01))
echo "  [OK] RBAC 已部署"

# ------------------------------------------------------------------
# Step 4: 构建/更新 ConfigMap
# ------------------------------------------------------------------
echo "[4/6] 构建 Alloy River 配置..."

# 创建临时目录 (rollback 和后续步骤共用)
TMPDIR=$(mktemp -d)

# 备份现有 ConfigMap (如果存在)
if kubectl -n "${NAMESPACE}" get configmap alloy-config &>/dev/null; then
  kubectl -n "${NAMESPACE}" get configmap alloy-config -o yaml > "${TMPDIR}/backup.yaml"
  CM_BACKUP="${TMPDIR}/backup.yaml"
  echo "  [INFO] 已备份现有 ConfigMap 到 ${CM_BACKUP}"
fi

# 读取现有用户列表
EXISTING_USERS=$(get_existing_users)
ALL_USERS="${EXISTING_USERS}"

# 检查用户是否已存在
if echo "${EXISTING_USERS}" | tr ',' '\n' | grep -qx "${USER_NAME}"; then
  echo "  [INFO] 用户 ${USER_NAME} 已存在，将更新其配置"
else
  if [[ -n "${EXISTING_USERS}" ]]; then
    ALL_USERS="${EXISTING_USERS},${USER_NAME}"
  else
    ALL_USERS="${USER_NAME}"
  fi
fi

# 生成 River 配置头部 (共享 discovery)
RIVER_CONFIG='// ============================================================
// ainspection Profiling — Alloy River 配置
// 管理方式: deploy/install.sh --user=<name> --pyroscope-endpoint=<ip>
// 当前用户: '"${ALL_USERS}"'
// ============================================================

// === 共享: Kubernetes Pod 发现 ===
discovery.kubernetes "ainspection_pods" {
  role = "pod"
  selectors {
    role = "pod"
    field = "spec.nodeName=" + sys.env("HOSTNAME")
  }
}
'

# 为所有用户生成管线配置
IFS=',' read -ra USER_ARRAY <<< "$ALL_USERS"
for u in "${USER_ARRAY[@]}"; do
  # 当前用户使用新的 endpoint, 其他用户保持原样
  # (此处简化为每次重新生成所有用户的配置)
  u_trimmed=$(echo "$u" | xargs)
  if [[ -z "$u_trimmed" ]]; then
    continue
  fi
  # 获取该用户之前的 endpoint (如果是已存在用户)
  if [[ "$u_trimmed" == "$USER_NAME" ]]; then
    u_endpoint="${PYROSCOPE_ENDPOINT}"
  else
    # 从现有 ConfigMap 中提取该用户的 endpoint
    u_endpoint=$(kubectl get configmap alloy-config -n "${NAMESPACE}" \
      -o json 2>/dev/null | \
      python3 -c "
import json, sys
data = json.load(sys.stdin)
config = data.get('data', {}).get('config.alloy', '')
for line in config.split('\n'):
    if 'ainspection_sink_${u_trimmed}' in line:
        # 在附近查找 url
        pass
# Fallback: use existing annotation
ann = data.get('metadata', {}).get('annotations', {})
eps = ann.get('ainspection.io/endpoints', '{}')
ep_map = json.loads(eps)
print(ep_map.get('${u_trimmed}', '${PYROSCOPE_ENDPOINT}'))
" 2>/dev/null || echo "${PYROSCOPE_ENDPOINT}")
  fi
  RIVER_CONFIG+=$(generate_user_river_blocks "${u_trimmed}" "${u_endpoint}")
  RIVER_CONFIG+=$'\n'
done

# 生成 ConfigMap YAML
cat > "${TMPDIR}/configmap.yaml" <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: alloy-config
  namespace: ${NAMESPACE}
  labels:
    app: alloy
  annotations:
    ainspection.io/users: "${ALL_USERS}"
    ainspection.io/endpoints: '{"${USER_NAME}": "${PYROSCOPE_ENDPOINT}"}'
data:
  config.alloy: |
$(echo "${RIVER_CONFIG}" | sed 's/^/    /')
YAML

if $DRY_RUN; then
  kubectl apply --dry-run=client -f "${TMPDIR}/configmap.yaml"
else
  if ! kubectl apply -f "${TMPDIR}/configmap.yaml"; then
    echo "  [ERROR] ConfigMap 部署失败"
    exit 1
  fi
fi
STEP_DONE=$((STEP_DONE | 0x02))
echo "  [OK] ConfigMap 已部署 (用户: ${ALL_USERS})"

# ------------------------------------------------------------------
# Step 5: 部署 DaemonSet
# ------------------------------------------------------------------
echo "[5/6] 部署 Alloy DaemonSet..."

if $DRY_RUN; then
  sed "s/__NAMESPACE__/${NAMESPACE}/g" "${PROJECT_DIR}/deploy/alloy/daemonset.yaml" | \
    kubectl apply --dry-run=client -f -
else
  if ! sed "s/__NAMESPACE__/${NAMESPACE}/g" "${PROJECT_DIR}/deploy/alloy/daemonset.yaml" | \
    kubectl apply -f -; then
    echo "  [ERROR] DaemonSet 部署失败"
    exit 1
  fi
fi
STEP_DONE=$((STEP_DONE | 0x04))
echo "  [OK] DaemonSet 已部署"

# ------------------------------------------------------------------
# Step 6: 等待就绪
# ------------------------------------------------------------------
echo "[6/6] 等待 Alloy DaemonSet 就绪..."
if $DRY_RUN; then
  echo "  [DRY-RUN] 跳过等待"
else
  kubectl -n "${NAMESPACE}" rollout status daemonset/alloy --timeout=120s || true
  echo ""
  echo "Alloy Pods:"
  kubectl -n "${NAMESPACE}" get pods -l app=alloy -o wide
fi

# ------------------------------------------------------------------
# 全部成功: 清理回退标记和临时文件
# ------------------------------------------------------------------
STEP_DONE=0
CM_BACKUP=""
PYROSCOPE_STARTED=false
trap - EXIT
[[ -n "${TMPDIR}" && -d "${TMPDIR}" ]] && rm -rf "${TMPDIR}"

echo ""
echo "============================================"
echo " 部署完成"
echo "============================================"
echo " Pyroscope:  ${PYROSCOPE_ENDPOINT}"
echo " 检查状态:   bash deploy/scripts/check.sh"
echo " 清理环境:   bash deploy/cleanup.sh"
echo "============================================"
