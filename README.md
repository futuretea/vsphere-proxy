# vSphere Proxy

面向内部程序的只读 vSphere 查询服务。管理员预先配置多个 ESXi 或 vCenter 环境，调用方通过 HTTP / JSON 查询一个指定环境。查询行为基于 `vsphere-mcp-server` v0.2.0；它的共同接口基线为 ESXi / vCenter 6.7、7.0.3 和 8.x，事件与告警等能力取决于目标。当前仓库尚未用真实目标验证这些版本。

## 启动

需要 Go 1.26.10 或兼容版本。将以下示例保存为 JSON 配置文件；用户名和密码只从指定的环境变量读取。

```json
{
  "listen": "127.0.0.1:8080",
  "targets": {
    "lab": {
      "endpoint": "https://vcenter.example.invalid/sdk",
      "username_env": "LAB_VSPHERE_USERNAME",
      "password_env": "LAB_VSPHERE_PASSWORD"
    }
  }
}
```

设置示例中指定的环境变量后运行：

```sh
go build -o vsphere-proxy ./cmd/vsphere-proxy
./vsphere-proxy --config config.json
```

可在 `targets` 中增加多个具名环境。配置在启动时读取，修改后重启服务。目标地址必须是 HTTPS，不能包含登录信息、查询参数或片段；默认校验证书，进程运行环境须信任目标证书。不要将真实凭证写入配置文件或提交到仓库。

对无法使用受信任证书的目标，可在该目标配置中显式设置 `"insecure": true`；不设置时默认为 `false`。跳过校验会使服务无法验证目标身份，登录信息可能发送给冒充目标的服务器，应仅在确认该风险可接受时启用。

服务自身没有调用方身份验证，也不提供 TLS。部署时必须位于受控内网，并由现有入口负责访问控制和传输保护；部署前验证该入口实际生效。示例只监听本机。

### 容器

```sh
docker build -t vsphere-proxy:dev .
docker run --rm -p 127.0.0.1:8080:8080 \
  -e LAB_VSPHERE_USERNAME -e LAB_VSPHERE_PASSWORD \
  -v "$(pwd)/config.json:/etc/vsphere-proxy/config.json:ro" \
  vsphere-proxy:dev
```

容器内的配置需将 `listen` 设置为 `0.0.0.0:8080`，并确保配置文件对容器中的非 root 用户可读。上述命令将端口仅映射到宿主机回环地址；服务依然需要可信的访问控制入口。镜像默认使用系统 CA 验证 vSphere 证书。

### Kubernetes / Helm

Chart 位于 `charts/vsphere-proxy`。部署前准备好镜像以及已有 Kubernetes Secret，Secret 的键名须与每个目标的用户名、密码环境变量名一致。例如，将以下配置保存为 `values-local.yaml`：

```yaml
image:
  repository: example/vsphere-proxy
  tag: dev
targets:
  lab:
    endpoint: https://vcenter.example.invalid/sdk
    username_env: LAB_USERNAME
    password_env: LAB_PASSWORD
existingSecrets:
  - vsphere-credentials
```

使用 `helm install lab charts/vsphere-proxy -f values-local.yaml` 安装。Chart 默认镜像名 `vsphere-proxy:dev` 是本地构建示例，不表示镜像已发布；无默认目标，必须提供至少一个目标和可用凭证。默认只创建 `ClusterIP` Service，不创建对外入口，部署者仍须确保已有访问控制。

如目标证书需要额外信任，可设置 `existingCaConfigMap` 为已有 ConfigMap 名称，并在其中提供 `ca.crt`；也可在单个目标下设置 `insecure: true` 跳过校验，其风险与本地配置相同。目标配置变化会触发滚动更新；Secret 或 CA ConfigMap 的内容变化后，需手动重启工作负载，例如 `kubectl rollout restart deployment/lab-vsphere-proxy`。

## 查询

所有查询使用 `GET /v1/targets/{target}/...`，返回 JSON；成功结果直接返回数组或对象。查询必须指定一个已配置的环境；多环境结果由调用方分别请求并合并。

| 路径 | 查询参数 | 用途 |
| --- | --- | --- |
| `/inventory` | 必需 `kind`；可选 `limit`、`offset`、`datacenter_id`、`name` | 查询资源列表 |
| `/resource` | 必需 `entity_type`、`entity_id` | 查询单个资源详情 |
| `/metrics` | 必需 `entity_type`、`entity_id`；可选 `limit` | 查询可用指标目录 |
| `/metric-samples` | 必需 `entity_type`、`entity_id`、`metrics`；可选 `instance`、`max_samples`、`interval_seconds`、`start_time`、`end_time` | 查询指标采样，多个指标重复传 `metrics` |
| `/tasks` | 可选 `limit`、`entity_type`、`entity_id`、`start_time`、`end_time` | 查询近期任务 |
| `/events` | 可选 `limit`、`entity_type`、`entity_id`、`start_time`、`end_time` | 查询事件 |
| `/alarms` | 可选 `limit` | 查询告警定义 |
| `/triggered-alarms` | 可选 `limit`、`entity_type`、`entity_id` | 查询当前告警 |

示例：

```sh
curl 'http://127.0.0.1:8080/v1/targets/lab/inventory?kind=vm&limit=2'
curl 'http://127.0.0.1:8080/v1/targets/lab/metric-samples?entity_type=VirtualMachine&entity_id=vm-123&metrics=cpu.usage.average&metrics=mem.usage.average'
```

`kind` 支持 `vm`、`host`、`datastore`、`network`、`datacenter`、`cluster`、`resource_pool`、`folder` 和 `distributed_portgroup`；具体可用资源取决于目标。`limit` 默认 100、最大 1000；库存 `offset` 默认 0，按路径、类型、ID 排序，库存变化可能影响分页。实体类型和 ID 在可选时须成对提供；时间使用 RFC3339。任务仅查询目标的近期任务缓存，按排队时间从新到旧返回。

指标采样默认取最近 1 点，最多 1000 点；返回的 `sample_info` 和指标值按下标对应，展示数值时将原始值乘以 `scale`。历史数据依赖目标的采集设置。

错误返回 `{"error":{"code":"...","message":"..."}}`：参数无效为 400，环境或路径不存在为 404，目标不支持该查询为 501，连接或目标查询失败为 502。服务不将上游错误细节或凭证返回给调用方。

目标明确返回登录会话失效时，服务为该环境重新建立会话，并重试一次当前只读查询；其他上游错误不会触发此恢复。恢复失败时返回 502。

## 验证

```sh
go test ./...
go vet ./...
go build ./...
```

本地测试使用 vSphere 模拟器，覆盖多目标隔离、8 类查询及目标能力差异；模拟器不能证明真实版本、证书和生产网络环境的行为。

`make ci` 还会运行竞态检测、golangci-lint、Helm lint 和模板渲染检查。GitHub CI 另外构建容器镜像并检查程序入口；这些检查不会发布镜像或部署到集群。

## 许可证

本项目使用 MIT 许可证，见 `LICENSE`。Go 运行时依赖的许可证与声明见 `THIRD_PARTY_LICENSES.md`。

容器镜像基于 Alpine Linux，系统组件及其许可信息见 [Alpine 软件包索引](https://pkgs.alpinelinux.org/packages)。镜像包含的 `GPL-2.0.txt` 为相关组件的许可证文本；本项目未附带 Alpine 组件源码。
