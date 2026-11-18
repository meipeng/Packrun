# Packrun

自建 Go 上传部署服务 + frp 外网访问。中控机纳管多台业务服务器，浏览器端完成
「选服务器 → 选目录 → 上传 zip/jar → 绑定脚本 → 执行 → 看回执」的完整链路。

```
外网浏览器
  │ HTTPS 443
  ▼
Nginx（可选：TLS、反代、限流、上传体积、真实 IP）
  │
  ▼
frp（可选：frps 公网 → frpc 中控机）
  │
  ▼
Go 中控服务（Gin + SQLite + 包仓库 + 内嵌前端）
  │ SSH（凭据按引用解析）
  ▼
业务服务器（部署目录 + 远程脚本）
```

## 目录

```
server/            Go 中控服务
  cmd/packrund/    入口
  configs/         配置样例
  internal/
    config/        配置加载（YAML + 环境变量覆盖）
    model/         实体定义
    store/         持久化与初始化
    auth/          口令、JWT、TOTP
    deploy/        执行器（local 沙箱 / SSH）
    api/           路由与处理器
    webui/         内嵌前端构建产物（make build 自动同步）
web/               Vue 3 + Vite + Element Plus 控制台
deploy/            Nginx 与 frp 配置样例
docs/              设计文档
Makefile           单包构建入口
start.sh           启动脚本（后台运行/停止、监听地址、子路径、Nginx 配置生成）
```

## 文档

| 文档 | 内容 |
|---|---|
| [docs/wiki/README.md](docs/wiki/README.md) | Code Wiki：项目概览、系统架构、后端模块、数据模型、API、执行引擎、认证安全、前端、运维与测试 |
| [docs/02-技术选型与客户端设计.md](docs/02-技术选型与客户端设计.md) | 架构定稿、框架选型、页面与接口约定 |
| [docs/03-部署与使用手册.md](docs/03-部署与使用手册.md) | 生产部署（构建 / systemd / Nginx / frp / 备份升级）、使用手册、已知限制与排查 |

## 本地启动

后端（Go 1.26+，依赖升级后 `go.mod` 已要求）：

```bash
cd server
go run ./cmd/packrund -config configs/config.yaml
# 监听 :9000，默认 deploy.mode=ssh（SSH 真实部署；开发可用 PACKRUN_DEPLOY_MODE=local 覆盖）
```

前端：

```bash
cd web
pnpm install
pnpm dev
# http://localhost:5173 ，/api 已代理到 127.0.0.1:9000
```

首次启动自动创建：

| 账号 | 口令 | 角色 |
|---|---|---|
| `admin` | `Admin@12345` | 管理员 |
| `auditor` | `Audit@12345` | 审计员（只读 + 审计，仅 debug 模式创建） |

登录后到「个人设置」绑定动态口令。**绑定后生产环境部署必须输入动态口令。**

## 单包构建与部署

生产不需要再分别部署前端与后端：前端构建产物会嵌入 Go 二进制。

```bash
make build
# 前端构建 → 同步到 server/internal/webui/dist → go build
# 产物：server/bin/packrund（单文件，前端已内嵌）
# 交叉编译：GOOS=linux GOARCH=amd64 make build
```

生成可直接上传服务器的部署包（默认 `linux/amd64`）：

```bash
make package                    # dist/packrun-linux-amd64.tar.gz
PKG_GOARCH=arm64 make package   # ARM 服务器
```

包内包含 `server/bin/packrund`、`server/configs/config.yaml`、`start.sh` 和
`deploy/` 样例配置，解压后即可按下方步骤启动。

把 `server/bin/packrund` 与 `configs/config.yaml` 拷到中控机，用 systemd 只托管这一个
进程即可。默认监听 `0.0.0.0:9000`、部署在 `/packrun` 子路径，可直接用
`http://<主机IP>:9000/packrun/` 访问；请用防火墙或安全组限制来源，并按实际拓扑调整
`server.trusted_proxies`。由同机 Nginx 反代时，把 `server.addr` 改回 `127.0.0.1:9000`
只暴露回环。
Nginx / frp 仅在需要 HTTPS 域名、公网穿透或独立限流时保留（见部署手册 §7、§8）。

仓库根目录的 `start.sh` 可直接改配置后启动：

```bash
./start.sh                 # 后台启动（默认；日志/PID 在 server/data/ 下）
./start.sh --stop          # 停止后台实例（--status 看状态，--log 跟日志）
./start.sh --foreground    # 前台启动，适合 systemd / supervisor 托管
./start.sh --print-nginx   # 按当前子路径打印 Nginx location 配置
```

默认部署在 `/packrun` 子路径（访问 `http://<主机IP>:9000/packrun/`）。如需部署在域名根路径：

```bash
PACKRUN_BASE_PATH=/ ./start.sh
PACKRUN_BASE_PATH=/ ./start.sh --print-nginx
```

`PACKRUN_BASE_PATH` 会同时作用于页面 `<base>`、前端资源、Vue 路由和 `/api/v1` 前缀，
无需重新构建前端。把脚本打印的 `location` 块放进 Nginx 的 `server {}` 即可；
`proxy_pass` 不要带尾斜杠，需保留 `/packrun/` 前缀交给应用处理。

## 两种执行模式

| 模式 | 说明 | 适用 |
|---|---|---|
| `local` | 文件真实落盘到 `data/sandbox/<服务器名>/<目录>`，zip 真实解压，脚本只渲染回显 | 本地开发、演示 |
| `ssh` | 通过 SSH 上传到目标机（`cat > tmp`）、`sha256sum` 二次校验、`unzip`/`mv`、远程执行脚本 | 生产 |

切换到 SSH：

```bash
export PACKRUN_DEPLOY_MODE=ssh
# 凭据支持三种方式：
#   直接录入：表单里直接填密码/私钥，AES-256-GCM 加密入库（需配置 security.credential_key）
#   file:~/.ssh/id_ed25519        （引用中控机上的密钥文件）
#   env:PROD_SSH_PASSWORD         （引用中控进程的环境变量）
```

主机指纹默认**首次连接自动信任（TOFU）**：首连成功自动登记并写审计日志，之后每次连接
据此校验，指纹不匹配即拒绝（服务器重建后在「服务器 → 指纹」清除重新信任）；设
`deploy.auto_trust_hostkey: false` 回退 `~/.ssh/known_hosts` 校验。仅开发环境可临时
`export PACKRUN_SSH_INSECURE=1`。

每台服务器的运行状态（负载/内存/磁盘/系统信息）由后台按
`deploy.metrics_interval_seconds`（默认 300 秒）定时采集，也可随时点「检测」即时采集；
「概览」页有各服务器资源汇总，部署向导选服务器时直接可见当前负载。

## 部署策略与任务可靠性

- **目录策略**：部署前校验目录占用是否超过 `maxSizeMB` 阈值；部署成功后按
  `keepReleases` 清理 `<目录>.bak-*` 旧备份（0 表示不保留）。
- **前后置脚本**：任务可同时绑定 `precheck` 前置校验脚本与 `postdeploy` 后置脚本；
  前置在解包前执行，失败即终止，不覆盖现有版本。
- **并发控制**：全局 `deploy.max_concurrent` 与每台服务器的 `maxConcurrent`
  同时生效，同一台服务器上的任务按上限排队；同一部署目录的任务强制串行。
- **目录变更保护**：任务创建时快照目标路径，排队期间目录被改动或删除时任务直接失败。
- **中断收口**：服务重启时，未完成的任务与运行中的步骤统一标记为失败，并写入
  `task.recover` 审计记录，不会永久停留在执行中。
- **脚本版本留痕**：任务记录前置/后置脚本版本，步骤日志保留执行时的脚本内容；
  排队期间脚本被删除时任务会直接失败，不会静默跳过。

## 安全约束落点

- 凭据只存引用（`servers.credential_ref`），数据库里没有明文。
- 动态口令支持绑定 / 换绑 / 解绑：支持认证器扫码或手动录入种子；换绑需用旧口令发起、
  新口令确认，确认前旧口令仍然有效。
- 登录恢复码：绑定动态口令时生成 10 枚一次性恢复码，仅展示一次；认证器不可用时
  可替代动态口令登录，重新生成会立即作废旧码。
- 生产环境发起部署走 step-up 再认证，服务端校验 TOTP，失败返回 `1428`。
- 部署目录强制绝对路径，拒绝 `..`；zip 解压前校验解压后体积不超过目录阈值，
  并拦截路径穿越、绝对路径、符号链接与越界文件（SSH 侧用 `zipinfo` 审查）。
- 登录失败 5 次锁定 5 分钟；`audit_logs` 记录真实 IP、动作、结果与动态口令校验状态。
- 数据范围：非管理员只能查看/操作 `serverScope` 内的服务器、目录、任务与概览；
  审计员只读，不能发起部署、取消任务或上传安装包。
- 项目目录绑定按 `serverScope` 校验，越权目录在创建任务与执行阶段都会被拒绝；
  非管理员查看服务器时 `credentialRef` 脱敏为 `***`，只有管理员可见原始引用。
- 前端高危操作二次确认，后端独立校验，不依赖前端。
- 生产启动强制校验：`server.mode` 非 `debug` 时必须通过 `PACKRUN_JWT_SECRET`
  提供 ≥32 字符随机密钥，并通过 `PACKRUN_ADMIN_PASSWORD` 提供管理员口令，
  否则服务拒绝启动；`config.yaml` 中的 dev 占位值只用于本地调试。
- `auditor` 演示账号（`Audit@12345`）仅在 `debug` 模式首次初始化时创建；
  已有环境请删除该账号，或改密并绑定 TOTP 后再使用。
- 真实 IP 依赖 `server.trusted_proxies`：仅当 Nginx / frpc 与中控同机且 18080
  未对公网开放时才保留回环地址；否则客户端可伪造 `X-Forwarded-For` 绕过登录锁定
  与审计 IP 记录，此时应清空该列表。
- 资源上限：单包 200MB（与 Nginx `client_max_body_size` 一致）；包仓库总容量
  由 `storage.max_total_mb` 限制（样例 20480MB）；单用户排队/执行中的任务数由
  `deploy.max_pending_per_user` 限制（样例 10）；解包时按目录 `maxSizeMB` 校验
  实际解压字节，并限制条目数 20000 条与默认 1GB 上限。
- 默认监听 `0.0.0.0:9000`：直连时必须用防火墙或安全组限制来源；经 Nginx / frp 反代时
  把 `server.addr` 改为 `127.0.0.1:9000` 只暴露回环，frps `proxyBindAddr = "127.0.0.1"`
  使 18080 仅监听本机，公网仅经 Nginx 的 443 进入；Nginx 侧启用 TLS 1.2/1.3、
  HSTS 与基础安全头，并对登录接口单独限流。
- 依赖漏洞复跑：

  ```bash
  # 后端
  cd server
  go run golang.org/x/vuln/cmd/govulncheck@latest ./...
  # 前端（仅生产依赖）
  cd web
  pnpm audit --prod
  ```

## 关键配置

`server/configs/config.yaml`：

```yaml
server:
  addr: 0.0.0.0:9000   # 默认所有网卡；前置 Nginx 时改为 127.0.0.1:9000
  trusted_proxies:      # 仅同机回环代理可信任，否则清空
    - 127.0.0.1/32
    - ::1/128
storage:
  max_total_mb: 20480   # 包仓库总容量上限
deploy:
  mode: local           # local | ssh
  max_concurrent: 3     # 全局并发闸门
  step_delay_ms: 350    # 沙箱模式步骤间隔，便于观察状态流转
  max_pending_per_user: 10 # 单用户待执行任务上限
```

环境变量覆盖：`PACKRUN_ADDR`、`PACKRUN_JWT_SECRET`、`PACKRUN_DEPLOY_MODE`、
`PACKRUN_ADMIN_PASSWORD`、`PACKRUN_MAX_CONCURRENT`。

## 生产前置

1. 设置 `PACKRUN_JWT_SECRET`（≥32 字符随机值）与 `PACKRUN_ADMIN_PASSWORD`，
   并把 `server.mode` 改为 `release`；缺少任一变量服务会拒绝启动。
2. 首次部署后立即修改 admin 口令并绑定动态口令；删除或改密 `auditor` 演示账号
   （该账号只在 `debug` 模式创建）。
3. 确认 `server.trusted_proxies` 与实际代理拓扑一致：仅同机 Nginx / frpc 时保留
   回环地址，否则清空，避免伪造 `X-Forwarded-For`。
4. 使用 Nginx / 负载均衡时：`client_max_body_size` 与服务端 200MB 上限保持一致，
   并启用 TLS 1.2/1.3、HSTS、CSP 与基础安全头；登录接口使用独立限流区域。
5. 使用 frp 时只暴露必要端口，`proxyBindAddr` 保持回环，18080 只允许 Nginx 访问，
   管理面与 18080 均不得对公网开放。
6. 中控机 `data/`（SQLite、包仓库）纳入备份；升级依赖后复跑 `govulncheck`
   与 `pnpm audit --prod`。
