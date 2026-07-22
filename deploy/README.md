# ainspection Profiling 基础设施

基于 **Grafana Alloy (k3s DaemonSet) + Pyroscope (docker)** 的生产级性能数据采集链路。

## 架构

```
研发本地 (10.106.19.42)
├── Pyroscope Server (docker, :4040)
│
└── k3s (node: 10.106.186.153)
    ├── 目标 Go 服务 Pods (qtmf, label app=xxx, pprof :80)
    └── Alloy DaemonSet (每 node 一个 Pod)
         ├── 发现: label app 存在 → 目标服务
         ├── 刮取: Pod IP:80/debug/pprof/*
         └── 写入: http://10.106.19.42:4040
```

## 环境约束

| 项目 | 值 | 说明 |
|------|-----|------|
| pprof 端口 | **80** | qtmf 框架固定 |
| 服务发现 | Pod label `app` | 所有 qtmf 管理 Pod 自带 |
| Pyroscope | 研发本地 docker | 默认 `10.106.19.42:4040` |
| k3s | 单节点 | `10.106.186.153` |

## 快速开始

### 1. 部署 (首次)

```bash
bash deploy/install.sh --user=alice --pyroscope-endpoint=http://10.106.19.42:4040
```

### 2. 追加开发者

多人在同一 k3s 调试时，每个开发者运行一次，Alloy 自动追加独立链路：

```bash
bash deploy/install.sh --user=bob --pyroscope-endpoint=http://10.106.19.43:4040
```

**隔离原理**: `discovery.kubernetes` (Pod 列表) 共享，其余每个开发者一套完整独立链路 (relabel → scrape → write)，互不干扰。

### 3. 查看状态

```bash
bash deploy/scripts/check.sh
```

### 4. 清理

```bash
bash deploy/cleanup.sh                # 清理 Alloy + Pyroscope
bash deploy/cleanup.sh --keep-pyroscope  # 只清理 Alloy, 保留 Pyroscope
```

## 采集的 Profile 类型

每个开发者独立采集 5 种 profile：

| 类型 | pprof 路径 | 采集间隔 |
|------|-----------|---------|
| CPU | `/debug/pprof/profile?seconds=15` | 60s |
| Heap | `/debug/pprof/heap` | 60s |
| Goroutine | `/debug/pprof/goroutine` | 60s |
| Mutex | `/debug/pprof/mutex` | 60s |
| Block | `/debug/pprof/block` | 60s |

## 文件结构

```
deploy/
├── README.md                     # 本文件
├── install.sh                    # 自动检测 + 部署
├── cleanup.sh                    # 一键清理
├── alloy/
│   ├── daemonset.yaml            # DaemonSet (ConfigMap 由 install.sh 动态生成)
│   └── rbac.yaml                 # SA + ClusterRole + Binding
├── pyroscope/
│   └── run.sh                    # docker run pyroscope
└── scripts/
    └── check.sh                  # 状态检查
```

## Alloy River 配置设计

### 命名规范

`ainspection_{user}_{component}`

示例: `ainspection_alice_cpu`, `ainspection_bob_heap`, `ainspection_alice_targets`

### 组件说明

| 组件 | 作用 |
|------|------|
| `discovery.kubernetes "ainspection_pods"` | 唯一共享组件，列出当前节点所有 Pod |
| `discovery.relabel "ainspection_{user}_targets"` | 过滤: Running + app label 存在 |
| `discovery.relabel "ainspection_{user}_{type}"` | 设置 `__address__`、`__profile_path__`、`service_name` |
| `pyroscope.scrape "ainspection_{user}_{type}"` | 按 `scrape_interval` 刮取 pprof 数据 |
| `pyroscope.write "ainspection_{user}_sink"` | 写入到指定 Pyroscope endpoint |

### ConfigMap 动态管理

`install.sh` 通过 ConfigMap annotation 追踪用户列表：

- `ainspection.io/users` — 逗号分隔的用户名列表
- `ainspection.io/endpoints` — JSON map: `user → endpoint`

每次执行 `install.sh`：
1. 读取现有 `ainspection.io/users`
2. 添加新用户
3. 为所有已知用户重新生成完整 River 配置
4. `kubectl apply` 更新 ConfigMap
5. Alloy 自动 reload (ConfigMap 变更检测)

## 手动部署 (分步)

```bash
# 1. RBAC
kubectl apply -f deploy/alloy/rbac.yaml

# 2. Pyroscope
bash deploy/pyroscope/run.sh

# 3. Alloy DaemonSet (ConfigMap 需先用 install.sh 生成，或手动创建 alloy-config)
kubectl apply -f deploy/alloy/daemonset.yaml

# 4. 验证
kubectl -n ainspection get pods -l app=alloy
curl http://localhost:4040/ready
```
