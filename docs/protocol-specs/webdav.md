# WebDAV 协议设计（P2 协议第一批 · 一）

> 状态：设计中 | 日期：2026-09-03
> 目标：让 NimbusDrive 可被操作系统与 rclone 等客户端**挂载为本地磁盘**（Windows 网络驱动器 / macOS Finder「连接服务器」/ rclone mount / 各类 WebDAV 客户端）。
> 立项依据：《立项决策与规划_v1.md》P2「WebDAV + S3 + HTTP 直链」，技术选型已定 `golang.org/x/net/webdav`（官方库）。

---

## 一、设计总览

```
Windows 映射驱动器 / rclone mount / Finder
        │  WebDAV 协议（HTTP 扩展方法 + XML）
        ▼
nginx :80 ──location /dav/──▶ TransferServer :8081  /dav/*
                                │  webdav.Handler（官方库，管 PROPFIND XML / Depth / LOCK）
                                │  Basic Auth 中间件（应用专用密码 → user_id）
                                ▼
                        webdav.FileSystem 适配器（本项目实现）
                                │
              ┌─────────────────┼──────────────────┐
              ▼                 ▼                  ▼
        FileRepo/           MinIO              QuotaRepo
        (files 表)      (对象读写/复制)      (上传配额)
```

核心思路：**协议翻译层，不动存储内核**。官方库 `webdav.Handler` 负责一切 XML/协议细节，我们只实现它的 `webdav.FileSystem` 接口，把 9 个接口方法桥接到现有 `FileRepo` / `MinIO` / `QuotaRepo`。复用度：元数据 CRUD、软删/回收站、配额、hash 引用计数全部现成。

---

## 二、关键决策

### D1 挂载位置：TransferServer 新增 `/dav` 路由组

**备选**：(a) APIServer (Gin)；(b) TransferServer (Hertz)；(c) 独立第三进程。

| 维度 | APIServer (Gin) | TransferServer (Hertz) ✅ | 第三进程 |
|------|----------------|--------------------------|---------|
| 字节流能力 | 无（只有元数据） | 已有 Range 流式下载、multipart 上传 | 自建 |
| 元数据 repo 访问 | ✅ | ✅（store 包共享，TransferServer 已注入 Repositories） | ✅ |
| 长连接隔离 | 与控制面混跑 | 与数据面混跑（同属 I/O 面，语义一致） | 最优但多一个部署单元 |
| MVP 成本 | 需在 Gin 侧新建整套流式读写 | 低 | 高（配置/观测/部署全套） |

**选 (b)**：WebDAV 的 GET/PUT 本质是数据面流量，与 TransferServer 职责一致；`store` 包是共享库，元数据操作不受进程边界限制。nginx 只加一条 `location /dav/`。

> 逃生通道：若挂载客户端长连接造成压力，后续可将 `/dav` 拆为独立监听端口甚至独立进程——FileSystem 适配器层不变，只挪装配代码。

**实现桥接**：`golang.org/x/net/webdav.Handler` 是标准 `http.Handler`，Hertz 侧沿用项目已有的 `common/adaptor` 桥接模式（`/metrics` 的 promhttp 已经这样接了），一行 `adaptor` 包装即可挂进 Hertz 路由。

### D2 认证：新增「应用专用密码」（app password），WebDAV 拒绝主密码

WebDAV 客户端（Windows 凭据管理器、rclone 配置文件、手机 App）会把 Basic Auth 明文凭据存到**系统层面**，安全边界远弱于浏览器。直接用主密码等于把主密码到处播撒，且无法单独吊销。

**方案**（坚果云/GitHub token 同款模型）：

- 新表 `app_passwords`（迁移 `0006_app_passwords.sql`）：

```sql
CREATE TABLE IF NOT EXISTS app_passwords (
  id            BIGSERIAL PRIMARY KEY,
  user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name          VARCHAR(64)  NOT NULL,             -- 用途备注，如 "Windows 笔记本挂载"
  password_hash VARCHAR(255) NOT NULL,             -- bcrypt
  last_used_at  TIMESTAMPTZ,
  revoked       BOOLEAN NOT NULL DEFAULT FALSE,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_app_passwords_user ON app_passwords(user_id);
```

- **Basic Auth 语义**：username = 主账号用户名，password = **应用专用密码**（bcrypt 比对）。传主密码一律 401（防止用户误用主密码形成习惯）。后续 S3 兼容层复用同一套凭据。
- 管理端点（JWT，挂 APIServer）：
  - `POST /api/v1/auth/app-passwords`（body: `{name}`）→ 响应**仅此一次**返回明文密码（16 字节随机 base64url）；库中只存 bcrypt。
  - `GET /api/v1/auth/app-passwords` → 列表（id/name/last_used_at/revoked/created_at）。
  - `DELETE /api/v1/auth/app-passwords/:id` → 置 `revoked = true`（保留审计痕迹，不物理删）。
- 校验成功后懒更新 `last_used_at`（每次 UPDATE 代价高，节流：与上次值差 > 1h 才写）。
- WebDAV Basic Auth 中间件实现于 `internal/middleware`，对 Hertz 侧生效；`realm = "NimbusDrive WebDAV"`。

### D3 路径解析：URL 路径逐段解析，不改表结构

WebDAV 的 URL 是深度路径（`/dav/文档/项目/计划.docx`），而 files 表是 `parent_id` 邻接表、**无 path 列**。两条路：

| 方案 | 优点 | 缺点 |
|------|------|------|
| 逐段解析 ✅ | 零 schema 变更；与现有唯一索引 `(user_id, parent_id, name)` 天然对齐 | 每请求 N 次查询（N = 路径深度，通常 ≤ 5） |
| 物化 path 列 | 单查询定位 | 迁移成本 + rename/move 级联重写子树 path，与现有递归 CTE 模型冲突 |

**选逐段解析**：`PathResolver` 从 root（parent_id = NULL）起逐段 `ListByParent` 按名字定位；挂载点交互以 PROPFIND Depth:1 为主，目录不深，N 次索引查询在 pgx 连接池下微秒级。Redis 缓存路径→ID 映射留作后续优化（失效条件复杂，MVP 不引入）。

**边界语义**：
- `/dav/` 根 = 该用户 parent_id IS NULL 的虚拟根目录，不对应任何 files 行。
- 名字含 `/` 的文件在 WebDAV 语义下**无法表达**（`/` 是路径分隔符）——现有 REST 创建文件名未做此校验，WebDAV 侧创建时拒绝（403），存量含 `/` 的名字挂载后不可见（记入已知 Gap）。
- URL 段百分号解码后参与匹配；PROPFIND 响应里的 href 按同样规则编码回写。
- Windows 挂载还有保留名（`CON`、`NUL` 等）问题，由客户端自处理，服务端不拦截。

### D4 PUT 写入路径：临时对象 + 流式哈希 + 服务端复制

现有上传协议是「check-hash → 分块 PUT → complete 合并」的会话式三步，WebDAV 的 PUT 是**单请求整文件字节流**，两者语义不同，不复用会话。同时 object key 依赖内容哈希（`blobs/{hash[:2]}/{hash[2:4]}/{hash}`），无法在收到字节前确定最终 key。

**写入流程**（`PutObject` 适配器内部）：

```
PUT /dav/path/file.ext
 1. 解析路径 → 确认父目录存在（不存在→409 Conflict，协议要求）
 2. 同名冲突处理（见下）
 3. Content-Length 配额预检：QuotaRepo.CheckUpload
 4. CreateMultipartUpload(tmp/{uuid})        ← 临时对象，流式不占内存
 5. 循环接收 Write()：io.TeeReader 同时算 sha256，按 16MiB 攒 UploadPart
 6. Close()：
    a. CompleteMultipartUpload(tmp)
    b. CopyObject(tmp → blobs/{sha256...})   ← MinIO 服务端复制，不过网
    c. RemoveObject(tmp)
    d. 事务：file_hashes.Upsert(hash, ...) + files.Create + MarkCompleted
       （秒传语义免费获得：同 hash 文件复用同一对象，仅引用计数 +1）
    e. users.used_storage += size
 7. 中途失败：AbortMultipartUpload，不留脏行
```

- **同名覆盖**（PUT 到已存在路径）：协议默认要求覆盖。覆盖 = 旧文件行 `SoftDelete`（进回收站）+ 新文件行插入——用户在回收站能找回落掉的旧版本，与产品删除语义一致。客户端发 `If-None-Match: *` 时存在即 412。
- **ETag = sha256 hex**（GET 响应头 + PROPFIND `getetag`），内容寻址存储天然强 ETag，为后续 `If-Match` 条件写打底。
- **配额拒绝**：超配额在步骤 3 就 507（Insufficient Storage，WebDAV 惯例码）。
- **大小上限**：单 PUT 走 nginx，`client_max_body_size` 需按网盘定位放宽（设计值 4G，nginx off 时注意 `proxy_request_buffering off` 避免落盘缓冲大文件）。

### D5 删除：软删进回收站，WebDAV 不提供硬删

`DELETE /dav/xxx` → 文件 `SoftDelete` / 文件夹 `SoftDeleteRecursive`，进现有回收站体系（宽限期、cron 清理、恢复全部复用）。挂载端看到的即「文件消失」，回收站语义对用户不可见但可从 Web UI 补救——比直接硬删安全得多，且**零新增代码**。

---

## 三、方法映射表（webdav.FileSystem 适配器）

| WebDAV 方法 | FileSystem 接口 | 桥接到的现有能力 | 备注 |
|-------------|----------------|-----------------|------|
| OPTIONS | （Handler 自带） | — | 官方库返回 DAV 头 |
| PROPFIND | Stat / OpenFile(R) + Readdir | `FileRepo.GetByID` / `ListByParent` | Depth 0/1 必须支持，infinity 显式拒绝（425/502，挂载客户端只用 0/1） |
| GET | OpenFile(R).Read | `MinIO.GetObject`（沿用 Range 206 逻辑） | Content-Length/ETag/Content-Type 从 files 行带出 |
| HEAD | 同上（Handler 处理） | 同上 | |
| PUT | OpenFile(W).Write + Close | D4 流程 | |
| MKCOL | Mkdir | `files.Create(is_folder=true)` | 父不存在→409；已存在→405 |
| DELETE | RemoveAll | `SoftDelete` / `SoftDeleteRecursive` | D5 |
| MOVE | Rename | 目标父目录相同→`FileRepo.Rename`；不同→`FileRepo.Move` + Rename | `Destination` 头解析 + `Overwrite: T/F` 头；文件夹移动仅改 parent_id，天然 O(1)（邻接表红利） |
| COPY | （阶段 3） | 文件：同 hash 新行 + `Upsert` 引用 +1；文件夹：递归 | 内容寻址使文件 COPY 是纯元数据操作，成本极低 |
| LOCK/UNLOCK | （Handler 自带） | `webdav.MemFS` 的锁实现借用 | 纯内存锁，重启即失效；Windows 挂载写入需要 LOCK 支持，借用官方实现即可 |

**FileInfo 映射**：`Name=files.name`、`Size=files.size`、`ModTime=updated_at`、`IsDir=is_folder`、`ETag=hash_sha256`、MIME=`mime_type`（文件夹固定 `inode/directory`，与现有 CreateFolder 一致）。

**目录树一致性**：适配器所有写操作走「先路径解析、后单行写」的乐观模式，不依赖事务锁多级路径。并发同名 PUT 的仲裁依赖现有唯一索引 `(user_id, parent_id, name) WHERE deleted_at IS NULL`——冲突方收到 409/500 重试。单用户挂载场景（本功能主要形态）冲突概率可忽略。

---

## 四、nginx 路由新增

```nginx
location /dav/ {
    proxy_pass http://nimbus_transfer;
    proxy_request_buffering off;   # 大文件 PUT 不落盘缓冲，直接流转上游
    client_max_body_size 4g;       # 单文件上限，与 PUT 设计值一致
    proxy_read_timeout 300s;
    proxy_send_timeout 300s;
}
```

放在 `/api/` 规则之前；`/dav/` 不走 `/api/` 归路由。

---

## 五、客户端兼容性已知事实

| 客户端 | 事实 |
|--------|------|
| Windows 资源管理器 | WebClient 服务默认**要求 HTTPS**，HTTP 挂载需改注册表 `BasicAuthLevel=2`。文档提供 rclone mount 作为推荐替代（HTTP 即可用） |
| rclone | `rclone mount :webdav: /mnt/nimbus`，HTTP + Basic Auth 开箱可用，作为主推路径 |
| macOS Finder | `cmd+K` 连接 `http://host/dav/`，HTTP 可用 |
| RaidDrive / RaiDrive 等 Windows 第三方 | HTTP 可用，常见替代 |

自建 HTTPS（caddy 自动证书）属于部署层后续事项，不在本分支范围。

---

## 六、分期计划

| 阶段 | 内容 | 交付标准 |
|------|------|---------|
| **W1 只读挂载** | 迁移 0006 + app_passwords 管理 API + Basic Auth 中间件 + PathResolver + FileSystem 适配器（只读方法）+ 挂载 TransferServer | rclone lsdir / Finder 浏览、GET/HEAD 下载全通 |
| **W2 可写** | PUT（D4 全流程）+ MKCOL + DELETE + MOVE | rclone copy 目录树双向校验通过（哈希比对） |
| **W3 收尾** | LOCK/UNLOCK（借 MemFS）、COPY（文件级）、`If-None-Match`、限速与配额联动测试 | Windows/RaiDrive 挂载可写；单测 + 集成测试绿 |

集成测试沿用 `//go:build integration` 模式：PathResolver 深路径解析、PUT→hash 引用→秒传复用、覆盖旧版本进回收站、MOVE 冲突、配额 507。

---

## 七、已知 Gap（显式不做，留后续）

1. **名字含 `/` 的存量文件**挂载后不可见（WebDAV 无法表达），不迁移不拦截 REST 侧创建。
2. **PROPFIND Depth: infinity** 不支持（协议允许拒绝；主流挂载客户端只用 0/1）。
3. **锁不持久**：LOCK 重启失效；无锁冲突仲裁（单用户挂载场景够用）。
4. **文件夹 COPY**、quota 超限的 WebDAV 报错本地化提示：留 S3 兼容层一起做。
5. **主密码直连 WebDAV**：永久拒绝（安全边界），文档明示引导创建应用密码。
