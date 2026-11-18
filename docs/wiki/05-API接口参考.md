# 05 API 接口参考

## 1. 通用约定

| 项 | 约定 |
|---|---|
| Base URL | `http(s)://<host><base_path>/api/v1`，默认 `base_path=/packrun` |
| 健康检查 | `GET /healthz`（始终在根路径，不受子路径影响） |
| 编码 | JSON（上传接口为 `multipart/form-data`） |
| 认证 | `Authorization: Bearer <JWT>` |
| 动态口令 | 请求体 `otp` 字段或请求头 `X-OTP`；部署向导两者都支持 |
| 统一响应 | `{"code":0,"message":"ok","data":...}` |
| 错误响应 | HTTP 状态码 + `{"code":<业务码>,"message":"..."}` |
| 分页 | `page` 从 1 开始，`pageSize` 默认 20、最大 100；返回 `{items,total,page,pageSize}` |
| 时间 | RFC3339 字符串 |

业务码：

| code | 含义 | 常见 HTTP |
|---|---|---|
| 0 | 成功 | 200 |
| 1001 | 认证失败/登录锁定 | 401 / 429 |
| 1002 | 令牌无效或已失效 | 401 |
| 1428 | 需要动态口令（step-up） | 401 |
| 1003 | 动态口令/恢复码错误 | 401 |
| 2001 | 权限不足 | 403 |
| 3001 | 参数错误 | 400 |
| 3002 | 资源不存在 | 404 |
| 3003 | 冲突（唯一键） | 409 |
| 4001 | 部署/配额失败 | 429 等 |
| 5001 | 服务端错误/容量不足 | 500 / 507 |

## 2. 接口总览

### 2.1 公开接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz` | 返回 `{"status":"ok"}` |
| POST | `/api/v1/auth/login` | 登录（口令 + 可选 TOTP/恢复码） |

### 2.2 认证接口（任意登录用户）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/auth/me` | 当前用户 |
| POST | `/api/v1/auth/password` | 修改口令 |
| POST | `/api/v1/auth/otp/setup` | 生成 TOTP 种子 |
| POST | `/api/v1/auth/otp/activate` | 绑定/确认换绑 TOTP |
| POST | `/api/v1/auth/otp/rebind` | 发起换绑（校验旧 OTP） |
| POST | `/api/v1/auth/otp/disable` | 解绑 TOTP |
| GET | `/api/v1/auth/recovery-codes` | 恢复码数量 |
| POST | `/api/v1/auth/recovery-codes/regenerate` | 重新生成恢复码 |

### 2.3 概览

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/overview` | 仪表盘统计、最近任务、服务器指标摘要 |

### 2.4 项目工作台

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/projects` | 登录用户 | 管理员看全部；其他用户只看自己作为成员的项目 |
| POST | `/api/v1/projects` | 非审计员 | 创建项目 |
| GET | `/api/v1/projects/:id` | 管理员/项目成员 | 项目详情（含目录与成员） |
| PUT | `/api/v1/projects/:id` | 管理员/项目 Owner | 改名/描述/目录绑定；成员与策略仅管理员 |
| DELETE | `/api/v1/projects/:id` | 非审计员 + 管理员/Owner | 删除项目（历史任务保留） |
| GET | `/api/v1/projects/:id/history` | 管理员/项目成员 | 按批次聚合的发布历史 |
| POST | `/api/v1/projects/:id/publish` | 非审计员 + 项目可读 + 目录在数据范围 | 批量发布 |
| POST | `/api/v1/projects/:id/rollback` | 同上 | 回滚到指定成功批次 |

### 2.5 服务器与目录

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/servers` | 登录用户 | 数据范围内的服务器列表（含指标，非管理员凭据脱敏） |
| GET | `/api/v1/servers/:id` | 数据范围 | 服务器详情 |
| POST | `/api/v1/servers/:id/ping` | 数据范围 | 连通性检测 |
| POST | `/api/v1/servers/:id/probe` | 数据范围 | 即时采集指标 + 凭据自检 |
| POST | `/api/v1/servers/:id/hostkey/probe` | 数据范围 | 采集主机指纹（仅 SSH） |
| POST | `/api/v1/servers` | 管理员 | 新增服务器 |
| PUT | `/api/v1/servers/:id` | 管理员 | 修改服务器 |
| DELETE | `/api/v1/servers/:id` | 管理员 | 删除服务器及其目录 |
| DELETE | `/api/v1/servers/:id/hostkey` | 管理员 | 清除已登记主机指纹 |
| GET | `/api/v1/directories?serverId=` | 数据范围 | 目录列表 |
| POST | `/api/v1/directories` | 管理员 | 新增目录 |
| PUT | `/api/v1/directories/:id` | 管理员 | 修改目录策略 |
| DELETE | `/api/v1/directories/:id` | 管理员 | 删除目录配置 |

### 2.6 脚本、包、任务、审计、用户

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/scripts` | 登录用户 | 脚本列表 |
| POST | `/api/v1/scripts` | 管理员 | 新增脚本 |
| PUT | `/api/v1/scripts/:id` | 管理员 | 修改脚本（版本 +1） |
| DELETE | `/api/v1/scripts/:id` | 管理员 | 删除脚本 |
| GET | `/api/v1/packages` | 登录用户 | 最近 50 个安装包 |
| POST | `/api/v1/packages` | 非审计员 | 上传 zip/jar（multipart） |
| GET | `/api/v1/tasks` | 数据范围 | 任务分页列表 |
| GET | `/api/v1/tasks/:id` | 数据范围 | 任务详情 + 步骤 |
| POST | `/api/v1/tasks` | 非审计员 + 数据范围 | 创建单目标部署任务 |
| POST | `/api/v1/tasks/:id/cancel` | 非审计员 + 数据范围 | 取消任务 |
| GET | `/api/v1/audit` | 管理员/审计员 | 审计日志分页 |
| GET | `/api/v1/users` | 管理员 | 用户列表 |
| POST | `/api/v1/users` | 管理员 | 新增用户 |
| PUT | `/api/v1/users/:id` | 管理员 | 修改用户 |

## 3. 认证接口

### 3.1 登录

`POST /api/v1/auth/login`

```json
{
  "username": "admin",
  "password": "Admin@12345",
  "otp": "123456"
}
```

`otp` 可为 6 位 TOTP 或 16 位恢复码（可带 `-`/空格）。

成功：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "token": "<JWT>",
    "user": {
      "id": 1,
      "username": "admin",
      "displayName": "平台管理员",
      "role": "admin",
      "otpEnabled": true,
      "serverScope": "*",
      "status": "active"
    }
  }
}
```

失败场景：

| 场景 | HTTP | code | message |
|---|---|---|---|
| 账号/口令错误 | 401 | 1001 | 账号或密码错误 |
| 已绑定 OTP 但未输入 | 401 | 1428 | 请输入动态口令 |
| OTP/恢复码错误 | 401 | 1003 | 动态口令无效，恢复码也可能已使用 |
| 失败次数过多 | 429 | 1001 | 失败次数过多，请 5 分钟后再试 |

失败计数维度：单账号 5 次、单 IP 30 次，锁定时长 5 分钟；成功后清零。

### 3.2 当前用户

`GET /api/v1/auth/me` → `data` 为用户对象（不含口令与 OTP 种子）。

### 3.3 修改口令

`POST /api/v1/auth/password`

```json
{ "oldPassword": "...", "newPassword": "至少8位", "otp": "123456" }
```

已绑定 OTP 时必须提供 `otp`。成功后 `token_version + 1`，所有旧 JWT 立即失效，
包括当前会话。

### 3.4 动态口令

| 接口 | 请求 | 成功返回 |
|---|---|---|
| `POST /auth/otp/setup` | 无 | `{secret, otpauth, issuer, period}` |
| `POST /auth/otp/activate` | `{code}` | `{enabled:true, recoveryCodes:[...]}` |
| `POST /auth/otp/rebind` | `{code}`（当前 OTP） | 新种子 `{secret, otpauth, issuer, period}` |
| `POST /auth/otp/disable` | `{code}` | `{enabled:false}` |
| `GET /auth/recovery-codes` | 无 | `{total, remaining}` |
| `POST /auth/recovery-codes/regenerate` | `{code}` | `{recoveryCodes:[...]}` |

换绑流程：`rebind`（旧 OTP）→ 新种子加入认证器 → `activate`（新 OTP）确认；
确认前旧 OTP 仍有效，待确认种子 10 分钟过期。

## 4. 概览

`GET /api/v1/overview` → `data`：

```json
{
  "serverTotal": 3,
  "serverOnline": 2,
  "taskToday": 12,
  "taskFailed": 1,
  "taskRunning": 2,
  "packageTotal": 8,
  "recentTasks": [ ... ],
  "deployMode": "ssh",
  "serverMetrics": [
    {
      "serverName": "app-prod-01",
      "env": "prod",
      "status": "online",
      "metric": {
        "load1": 0.42,
        "memTotalMB": 8192,
        "memUsedMB": 3200,
        "dirUsage": "[{\"path\":\"/\",\"totalMB\":102400,\"usedMB\":51200,\"pct\":50}]",
        "probedAt": "2026-11-16T10:00:00+08:00"
      }
    }
  ]
}
```

所有统计均按当前用户的 `serverScope` 过滤；`packageTotal` 为全局包数量。

## 5. 项目接口

### 5.1 项目对象

```json
{
  "id": 1,
  "name": "订单服务",
  "code": "order-service",
  "description": "订单核心服务",
  "ownerId": 1,
  "version": "v0.0.3",
  "status": "active",
  "createdAt": "...",
  "updatedAt": "...",
  "dirs": [
    {
      "id": 1,
      "serverId": 1,
      "serverName": "app-prod-01",
      "serverEnv": "prod",
      "path": "/opt/apps/order-service",
      "allowOverwrite": true,
      "backupBeforeDeploy": true,
      "keepReleases": 5,
      "maxSizeMB": 512
    }
  ],
  "members": [
    { "id": 2, "username": "deployer1", "displayName": "发布者一", "role": "deployer" }
  ]
}
```

### 5.2 创建 / 修改

`POST /api/v1/projects`

```json
{ "name": "订单服务", "code": "order-service", "description": "可选" }
```

`code` 会转小写并仅保留 `a-z0-9-`；重复返回 409。

`PUT /api/v1/projects/:id`

```json
{
  "name": "可选",
  "description": "可选",
  "directoryIds": [1, 2],
  "memberIds": [2, 3],
  "policy": {
    "backupBeforeDeploy": true,
    "allowOverwrite": true,
    "keepReleases": 5
  }
}
```

- `directoryIds`：整体替换绑定；任一目录不存在或超出数据范围则全部拒绝。
- `memberIds`：仅管理员；整体替换，自动跳过 Owner。
- `policy`：仅管理员；同步写入全部绑定目录，`keepReleases` 限制在 1-20。

### 5.3 发布

`POST /api/v1/projects/:id/publish`

```json
{
  "packageId": 8,
  "preScriptId": 2,
  "scriptId": 1,
  "otp": "123456"
}
```

成功返回：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "batchNo": "PR20261116103000ABCDEF",
    "version": "v0.0.4",
    "tasks": [ { "id": 101, "taskNo": "D..." } ]
  }
}
```

约束：

- 项目仍有进行中批次 → 400；
- 未绑定目录 → 400；
- 任一目录不在数据范围 → 403；
- 任一目录服务器为 `prod` → 必须提供有效 `otp`，否则 401 / code 1428；
- 单用户待执行任务配额不足 → 429 / code 4001，且不创建任何任务；
- 包记录或包文件不存在 → 400。

### 5.4 回滚

`POST /api/v1/projects/:id/rollback`

```json
{ "batchNo": "PR20261115120000ABCDEF", "otp": "123456" }
```

回滚规则：

- 源批次必须属于当前项目；
- 项目当前版本等于源批次版本 → 400；
- 源批次包记录与包文件必须仍存在；
- 复用源任务绑定的前置/后置脚本（若仍存在）；
- 新任务 `kind=rollback`，`remark="回滚自 <当前版本>"`；
- 批次全部成功后才把项目版本改为源版本。

### 5.5 历史

`GET /api/v1/projects/:id/history` → 数组，每项：

```json
{
  "batchNo": "PR...",
  "kind": "deploy",
  "projectVersion": "v0.0.3",
  "packageId": 8,
  "packageName": "order-service.zip",
  "packageSize": 1048576,
  "sha256": "...",
  "operator": "admin",
  "remark": "",
  "status": "success",
  "createdAt": "...",
  "tasks": [
    {
      "id": 101,
      "taskNo": "D...",
      "serverName": "app-prod-01",
      "targetPath": "/opt/apps/order-service",
      "status": "success",
      "stage": "done",
      "progress": 100,
      "message": "部署完成"
    }
  ]
}
```

## 6. 服务器接口

### 6.1 服务器对象

```json
{
  "id": 1,
  "name": "app-prod-01",
  "env": "prod",
  "host": "10.0.10.11",
  "port": 22,
  "username": "deploy",
  "authType": "key",
  "credentialRef": "***",
  "status": "online",
  "maxConcurrent": 1,
  "remark": "生产应用节点",
  "hostKeyAlgo": "ssh-ed25519",
  "hostKeyFingerprint": "SHA256:...",
  "hostKeyPinnedAt": "...",
  "metrics": { }
}
```

非管理员始终看到 `credentialRef="***"`；管理员可见原始引用。

### 6.2 新增 / 修改

`POST /api/v1/servers`、`PUT /api/v1/servers/:id`

```json
{
  "name": "app-prod-01",
  "env": "prod",
  "host": "10.0.10.11",
  "port": 22,
  "username": "deploy",
  "authType": "key",
  "credentialRef": "file:~/.ssh/id_ed25519",
  "maxConcurrent": 1,
  "remark": "生产应用节点"
}
```

校验：名称仅 `A-Za-z0-9._-`；环境 `dev/test/prod`；认证方式 `key/password`；
凭据必须以 `env:` 或 `file:` 开头；端口默认 22、并发默认 1。

### 6.3 检测与指标

`POST /api/v1/servers/:id/ping`：

```json
{ "status": "online", "message": "连接正常", "mode": "ssh" }
```

`POST /api/v1/servers/:id/probe`：

```json
{
  "status": "online",
  "message": "连接正常",
  "metric": { "hostname": "...", "os": "...", "load1": 0.4, "memTotalMB": 8192, "memUsedMB": 3200, "dirUsage": "[...]" },
  "credential": { "ok": true, "message": "凭据可解析（ssh-ed25519 私钥）" },
  "mode": "ssh"
}
```

`POST /api/v1/servers/:id/hostkey/probe`：

```json
{ "algo": "ssh-ed25519", "fingerprint": "SHA256:...", "pinned": true, "matched": true }
```

`DELETE /api/v1/servers/:id/hostkey` → `{"cleared":true}`。

## 7. 目录接口

`GET /api/v1/directories?serverId=1` → 目录数组。

`POST /api/v1/directories`：

```json
{
  "serverId": 1,
  "path": "/opt/apps/order-service",
  "owner": "app",
  "allowOverwrite": true,
  "backupBeforeDeploy": true,
  "keepReleases": 5,
  "maxSizeMB": 512,
  "remark": "生产目录"
}
```

- `path` 必须绝对路径，拒绝 `..` 与根目录；
- 同一服务器下路径唯一；
- `maxSizeMB<=0` 时按 1024 处理。

`PUT /api/v1/directories/:id` 使用同样的字段（`serverId` 不参与更新）；
`DELETE /api/v1/directories/:id` → `{"deleted":true}`。

## 8. 脚本接口

`GET /api/v1/scripts` → 按 `phase,id` 排序。

`POST /api/v1/scripts`：

```json
{
  "name": "重启应用",
  "phase": "postdeploy",
  "content": "systemctl restart order-service",
  "timeoutSec": 60,
  "runAs": "app",
  "description": "部署后重启"
}
```

校验：名称非空、`phase` 为 `precheck/postdeploy`、内容非空、超时 1-3600 秒。
创建时 `version=1`，更新时 `version+1`。

## 9. 安装包接口

`GET /api/v1/packages` → 最近 50 条记录。

`POST /api/v1/packages`（`multipart/form-data`，字段名 `file`）：

- 仅 `.zip` / `.jar`；
- 单包 ≤ 200MB；
- 文件头必须为 `PK\x03\x04`；
- 包仓库总容量受 `storage.max_total_mb` 限制，超限返回 HTTP 507 / code 5001；
- 成功返回包记录（含 `sha256`）。

```json
{
  "id": 8,
  "name": "order-service.zip",
  "kind": "zip",
  "size": 1048576,
  "sha256": "...",
  "createdAt": "..."
}
```

## 10. 任务接口

### 10.1 创建任务

`POST /api/v1/tasks`

```json
{
  "serverId": 1,
  "directoryId": 1,
  "packageId": 8,
  "preScriptId": 2,
  "scriptId": 1,
  "otp": "123456"
}
```

`preScriptId` / `scriptId` 可省略。校验顺序：

1. 审计员拒绝；
2. 服务器存在且在数据范围；
3. 目录存在且属于该服务器；
4. 安装包存在；
5. 前置脚本必须 `precheck`，后置脚本必须 `postdeploy`；
6. `env=prod` 必须通过 step-up；
7. 单用户待执行任务配额；
8. 落库并异步执行。

成功返回任务对象（`status=pending`、`stage=queued`）。

### 10.2 查询

`GET /api/v1/tasks?page=1&pageSize=20&status=success&serverId=1&projectId=1&batchNo=PR...&keyword=xxx`

`keyword` 匹配任务号、包名、操作人。

`GET /api/v1/tasks/:id` → `{ "task": {...}, "steps": [...] }`。

### 10.3 取消

`POST /api/v1/tasks/:id/cancel` → `{"canceled":true}`。

- 终态任务 → 400；
- `pending` → 直接写 `canceled`；
- 运行中 → 触发上下文取消，执行器返回后归为 `canceled`；
- 已无运行进程 → 400「任务进程已结束」。

## 11. 审计接口

`GET /api/v1/audit?page=1&pageSize=20&action=task.&result=fail&actor=admin&keyword=app-prod-01`

- `action` 前缀匹配；
- `result` 精确匹配 `ok/fail`；
- `actor` 模糊匹配；
- `keyword` 匹配 `target` 或 `task_no`。

返回分页对象，`items` 为审计记录数组。

## 12. 用户接口

`GET /api/v1/users` → 全部用户（管理员）。

`POST /api/v1/users`：

```json
{
  "username": "deployer1",
  "displayName": "发布者一",
  "password": "至少8位",
  "role": "deployer",
  "serverScope": "1,2",
  "parentId": null,
  "status": "active"
}
```

`PUT /api/v1/users/:id`：字段同上，`password` 为空表示不重置口令。
后端保证至少保留一个启用状态的管理员；口令、角色、范围或状态变化会使该用户
所有旧 JWT 失效。

## 13. 权限矩阵

| 操作 | admin | deployer | auditor |
|---|---|---|---|
| 查看服务器/目录/脚本/包/任务 | ✓ | ✓ | ✓ |
| 连通性检测/指标采集/指纹采集 | ✓ | ✓ | ✓ |
| 发起部署 / 取消任务 | ✓ | ✓ | ✗ |
| 上传安装包 | ✓ | ✓ | ✗ |
| 创建/删除项目 | ✓ | ✓（创建后需被管理员加入成员，否则列表不可见） | ✗ |
| 修改项目目录绑定 | ✓ | ✓（Owner） | ✗ |
| 修改项目成员/发布策略 | ✓ | ✗ | ✗ |
| 发布 / 回滚 | ✓ | ✓（项目成员） | ✗ |
| 服务器/目录/脚本/用户配置 | ✓ | ✗ | ✗ |
| 查看审计 | ✓ | ✗ | ✓ |

数据范围 `serverScope` 对所有角色（除管理员）生效；项目访问还受成员关系限制。
脚本与安装包当前没有按项目或服务器范围隔离，所有登录用户可见。
审计员的 ping/probe/hostkey 检测虽然不放行部署，但会更新服务器状态并写审计日志。

## 14. 前端调用映射

前端 API 模块位于 [web/src/api/index.ts](../../web/src/api/index.ts)，与本文接口一一对应：

| 模块 | 覆盖接口 |
|---|---|
| `authApi` | 登录、当前用户、改密、OTP、恢复码 |
| `serverApi` | 服务器 CRUD、ping、probe、主机指纹 |
| `directoryApi` | 目录 CRUD |
| `scriptApi` | 脚本 CRUD |
| `packageApi` | 包列表、上传（带进度） |
| `taskApi` | 任务列表/详情/创建/取消 |
| `projectApi` | 项目 CRUD、历史、发布、回滚 |
| `auditApi` | 审计分页 |
| `userApi` | 用户列表/创建/修改 |
| `overviewApi` | 概览 |
