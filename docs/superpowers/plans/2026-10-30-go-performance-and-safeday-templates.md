# 并发性能优化与安全日模板 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. All subagents use DeepSeek.

**Goal:** 完成树障 WXML 格式化、两个既有缺陷修复，并在不改变商旅代理的前提下实施有界高并发、全应用性能优化及安全日可配置模板。

**Architecture:** 在现有 Go/Vue/小程序架构内增量修改。共享配置缓存、连接复用和有界异步日志先降低基础设施开销；各业务以键控锁、信号量和 worker 池提高并行度；安全日模板以数据目录 JSON 元数据、内容寻址 docx 和记录快照实现，内置模板与既有 Dify `url/date/class` 契约保持兼容。

**Tech Stack:** Go 1.27+ / chi / `database/sql` / `sync` / `httptest`，Vue 3 + Vite + TDesign，原生 WXML/WXSS，JSON + docx（ZIP/XML）。

**Spec:** 用户本轮四项指示；上一已批准计划中的优化评估与安全日模板设计，以及本轮 DeepSeek 只读实施调查。

## Global Constraints

- 不修改 `uvmp-toolkit/`；不部署、不连接真实云服务、不执行 commit/push/merge/tag。
- 商旅显式代理和 fail-closed 口径完全不变；不实施日常流量直连。
- 并发必须可配置且有上限；同一成员/日期的商旅费用及打卡 RMW、同一照片任务、同一记录写回必须互斥。
- 不追求无上限 goroutine；不盲目并发微信消息、事务、SSE、ZIP/PDF 输出等有顺序要求的链路。
- Go 新行为先写失败测试并确认 RED，再最小实现至 GREEN；全部外部服务使用 fake/httptest。
- 不新增第三方依赖；Web 不新增测试脚手架。Go 用 gofmt，JS/Vue/WXSS 用 Prettier；树障 WXML 的整文件格式化是用户独立明确要求，不混入其他文件格式化。
- 安全日同班组同日期仍只保留最新记录；默认模板、既有记录和不传 `template_id` 的小程序/静态回退端保持原行为。
- 修改配置、路由、数据目录或性能口径时同步 README、开发指南、docker/.env.example 及必要的行内注释；不修改 AGENTS.md 中未变化的规则。

## Review Focus

- 并发锁顺序与任务收尾：商旅单元池 → 成员/日期键控锁 → HTTP；不得反向嵌套，所有任务在错误、取消和 panic 后释放槽位并置终态。
- 缓存正确性：空字符串也是已配置值；`sql.ErrNoRows` 保真；Update/Seed 先失效再 notify；安全日 List/快照必须返回副本。
- 异步日志可靠性：info 满队可丢弃并计数，warn/error 满队最多等待 20ms 后写 stderr；关闭时冲刷，不使用已取消请求 context。
- 模板兼容与数据安全：模板校验先于替换旧记录；docx 内容寻址不可覆盖；记录创建时一次写入快照；未知模板/损坏 docx 不得删除旧记录。
- 格式化与性能改动分离：树障 WXML 只允许格式变化；业务逻辑不得藏在大格式 diff 中。

## 默认参数

- 商旅 `SGCC_SYNC_CONCURRENCY=4`（1–8）、`SGCC_PHOTO_CONCURRENCY=2`（1–4）；旧 `SGCC_SYNC_INTERVAL_MS` 不再读取。
- 配置缓存 TTL 60s、负缓存 10s、最多 512 键；日志队列 8192 条、4 writer、warn/error 入队等待 20ms。
- MySQL `MYSQL_MAX_OPEN_CONNS=32`、`MYSQL_MAX_IDLE_CONNS=16`、空闲连接 5 分钟；HTTP 全局 128 / 每主机 32 空闲连接。
- 出工日志验证 4 路、过期巡检每 10 分钟；网盘 dirsize 缓存 5 分钟/128 条、ZIP 预取深度 1。
- 安全日模板最多 50 套，每套 1–12 段，docx ≤10MB；模板全局共享，超管管理，safe-day 用户生成时可选。

---

### Task 1: 格式化树障 WXML

**Files:** `miniprogram/pkg-treehazard/pages/index/index.wxml`

- [ ] 将 WXML 临时复制为 `.html`，用 `npx --yes prettier@3 --write --parser html` 格式化后覆盖回原文件，删除临时文件。
- [ ] 用 `git diff --word-diff=porcelain` 复核只含排版和已存在的 `sheet-body ed-body`，不改属性值、事件或结构；再对临时 `.html` 副本执行 `--check`，Expected: PASS。

### Task 2: 商旅部分异常数据 fail-closed

**Files:** `go/internal/apps/sgccclockin/photoreconcile.go`、`photoreconcile_test.go`

- [ ] 新增 `TestSyncFeePhotosPartialInvalidRemoteSkips`：远端含两个有效 ID 和一个无 ID 异常项，本地含对应照片；旧实现会删除异常项对应本地照片，测试应先 FAIL。
- [ ] 任一远端元素 `ParseFeeImageItem` 返回 nil 时，记录样例键和 fail 日志并立即跳过整个照片对账；不得执行下载、DB 删除或 COS 删除。
- [ ] 运行 sgccclockin 全包测试，Expected: PASS。

### Task 3: 修复 demo 响应式对象克隆

**Files:** `web/src/api.js`

- [ ] 将 demo `delay` 的克隆改为 JSON round-trip，把 Vue Proxy 转为普通 JSON 数据；不改变 120ms 延迟和 live 适配器。
- [ ] `npm run build`，并用 demo 浏览器回归登录、刷新、profile、树障与管理配置读取，Expected: 无 DataCloneError。

### Task 4: 配置缓存、数据库池与 HTTP 复用

**Files:** `go/internal/config/store.go` 及测试、`go/internal/integrations/transport.go` 及测试、`go/cmd/shade/main.go`、`docker/.env.example`、README/开发指南（最后统一文档任务也可承接）。

- [ ] TDD 覆盖同键只查一次、空值命中、ErrNoRows 负缓存、DB 错误不缓存、Update/Seed 先失效再 notify、64 goroutine 同键合并、敏感值脱敏。
- [ ] Store 增加 RWMutex 缓存和 per-key single-flight，TTL 60s/10s、最多 512 键；PublicValue 复用 Get。
- [ ] 新增共享 Transport 工厂，MaxIdleConns=128、MaxIdleConnsPerHost=32、IdleConnTimeout=90s；生产装配时设为 `http.DefaultTransport`，使现有默认 client 全部复用。商旅 directTransport 克隆后仍 Proxy=nil/DisableCompression=true，代理传输补 MaxIdle=64/PerHost=16。
- [ ] MySQL DSN 自动拼装分支追加 `timeout=5s&interpolateParams=true`；池默认 32/16、ConnMaxIdleTime=5m，两个池上限走 env；MYSQL_DSN 直填保持原样。
- [ ] 运行 config/integrations/cmd 测试及 race 测试。

### Task 5: 有界异步日志与身份复用

**Files:** `go/internal/logging/`、`go/internal/httpapi/auth.go`、`accesslog.go` 及测试、`go/internal/integrations/recorder.go`（如需）、`go/cmd/shade/main.go`（由集成任务统一合并）。

- [ ] TDD 覆盖异步写/Flush、info 满队非阻塞丢弃、warn/error 20ms 后 stderr、失败重试一次、panic 隔离、Close 冲刷且幂等、取消的请求 ctx 不影响落库。
- [ ] 新增 `logging.Sink` 和 `Queue`：容量 8192、4 writer；保留 DirectSink 供测试/兜底；`New(db,appCode)` 兼容，新增 `NewWithSink`。
- [ ] AuthMiddleware 验签后在 admin gate 前把 identity 写入共享 carrier；WithAccessLog 优先复用，403 不重复查 sys_user，401 不查用户。保留现有 WithAccessLog 签名兼容测试，新增可注入 Sink/Auth 的 Options。
- [ ] 生产所有 app logger、integrations recorder 和 access log 接同一 Queue；scheduler/微信停止后 Close/Flush，再关闭 DB。healthz 不依赖队列，panic 仍原样上抛。

### Task 6: 商旅去间隔与高并发

**Files:** `go/internal/apps/sgccclockin/`（protocol/proxy/runtimecfg/module/sync/remind/photoop/remote 及测试）、`web/src/features/sgccclockin/components/SyncPullDialog.vue`、`web/src/views/AdminConfigView.vue`、README/开发指南。

- [ ] TDD 证明 CallJSONX HTTP 期间不持池锁、并发 uniID 唯一、补池 single-flight；旧间隔 3000 不再读取；同步峰值≤4、不同成员并行、同成员/日期串行、照片跨卡并行/同卡互斥、panic 解锁。
- [ ] CallJSONX 对齐 CallJSONM：锁只保护 uniID 池/指纹，HTTP 在锁外；补池双检去重，所有解锁路径 defer。
- [ ] 新增引用计数 keyedMutex 和有界 worker/semaphore。`SyncOne` 全局默认 4（1–8）并对 member+date 加锁；syncTeamDay、提醒前同步、每日核查和保鲜使用同一上限。feePost/clockinPost/link/removeFeeImageRemote 使用相同键覆盖完整 RMW。
- [ ] PhotoOp 全局 tail 改默认 2（1–4）信号量，保留同卡 activeLock；增加 panic 终态与解锁。照片内部逐成员顺序不变。
- [ ] 删除 sleepInterval 及全部调用，旧配置键标 Deprecated 不再读取；新增两个并发键和后台标签。Web 多成员同步改并发发起，由后端门限控制，不再逐请求等待形成串行。
- [ ] fakeAPI 共享计数加锁；运行 `go test -race ./internal/apps/sgccclockin` 和全包测试。

### Task 7: 出工日志与树障性能

**Files:** `go/internal/apps/worklog/`、`go/internal/apps/treehazard/`、`go/cmd/shade/main.go`（仅集成任务接入 sweeper）。

- [ ] TDD 覆盖列表/附件/PDF 每请求只解析一次 PublicURL，空 base 回退不变；loadEntries 不再 UPDATE pending；sweeper 排除在飞照片且 stop 幂等；验证峰值≤4；树障 COS 删除全部发生在 Commit 后。
- [ ] worklog 增加 URL base 复用；runVerify/VerifyAndWriteBack 内统一 4 路信号量和在飞集合，商旅回调路径也受限。
- [ ] pending UPDATE 移到启动即跑＋每 10 分钟 sweeper；schema 新增并幂等补 `(verify_status,created_at)` 索引。
- [ ] treehazard attachPhotos 复用 base；暂存导入事务只保留 DB，COS key 收集后 Commit 再删除，失败仅记录且不回滚已导入数据。

### Task 8: 网盘性能

**Files:** `go/internal/apps/netdisk/` 及测试。

- [ ] TDD 覆盖 10 个并发首登仅一次 login、单次 Call 配置读取≤3、dirsize 命中/写失效/同 key 单飞、ZIP 峰值≤2且条目顺序不变。
- [ ] OpenList 每次操作只解析一次配置，login mutex 内双检 token；401 只重试一次，47h TTL/60s 失败冷却不变。
- [ ] dirsize 增加 5 分钟/128 条缓存和同 key single-flight，leader 使用 WithoutCancel+35s；所有写操作收口失效。
- [ ] zipWalk 保持顺序和 zip.Store，仅深度 1 预取下一文件，所有错误路径关闭在飞流并保持 `panic(http.ErrAbortHandler)`。

### Task 9: 安全日 RecordStore 与模板后端

**Files:** `go/internal/apps/safeday/`、`go/internal/compat/exceldoc/`、`go/internal/integrations/dify.go`、`difyadapters.go`、`server/assets/safeday/`。

- [ ] TDD 覆盖 RecordStore 副本/并发读写/落盘重载、动态章节顺序与标题/空段/转义/20000字、内置兼容、模板校验/CRUD/默认/权限/快照/旧回调、不同模板 API key、并发渲染。
- [ ] RecordStore 改 RWMutex+缓存副本，写副本→原子写盘→换 cache；DocxRenderer 去掉无锁单槽缓存。
- [ ] 模板存 `templates/index.json` 和内容寻址 `templates/files/{sha256}.docx`；内置 id=builtin/static 六段，自定义 dynamic 必须含独占 `{@章节区}`。新增标题/章节块 XML 函数和动态样例 docx。
- [ ] 自定义模板包含 Dify API key 设置。复用现有 `config.ConfigCipher` 以 `enc:v1:` 密文存 index.json；接口只返回 `hasApiKey`，绝不返回明文/密文。新建必须提供 key，编辑留空保留原 key；记录快照、日志和错误文案不得包含 key。内置模板使用全局 `DIFY_SAFEDAY_API_KEY`。
- [ ] `WorkflowInput`/`StreamInput` 增加仅本次调用生效的 APIKey override；`DifyWorkflow` 与 `WorkflowClient.Start` 在读取默认配置后优先使用 override，空值保持原全局逻辑，日志不得输出 override。
- [ ] 管理 API 挂 `/api/v1/admin/safeday/templates*`，仅 admin；普通 GET `/api/v1/safeday/templates` 供生成选择。支持列表、新建、编辑名称/章节/API key、替换 docx、设默认、删除、下载/样例。
- [ ] generate 接受 `template_id`，模板及 key 校验先于 Create；Record 一次写入模板名/模式/sha/sections 快照（不含 API key）；FormDefaults 可记忆模板。callback 按快照 key 取扁平字段，也兼容 sections 对象/JSON 字符串；Dify 输入仍仅 url/date/class。
- [ ] 模板改删不影响在途记录；未知模板不替换旧记录；缺失 docx/key 明确 failed。静态回退和小程序不传 id 继续 builtin。

### Task 10: 安全日模板 Web

**Files:** `web/src/features/safeday/`、`web/src/views/SafedayView.vue`。

- [ ] 新增模板 API 与管理组件：仅超管可见，模板列表、名称、可增删/排序的 key+title 行、Dify API key 密码输入（新建必填、编辑留空保持不变、只显示已配置状态）、上传/替换 docx、设默认、删除、下载样例；沿用现有 token 和每页一个主按钮。
- [ ] 生成抽屉增加模板选择并提交 template_id，记录行显示模板名；demo 提供虚构模板且不含真实 key，写操作仍拒绝。
- [ ] `npm run build`，浏览器回归超管/普通用户、默认模板、API key 不回显、增删排序章节、表单校验和生成参数。

### Task 11: 集成、文档与最终验证

- [ ] 合并 main.go 装配：共享 Transport、日志 Queue、各 logger、WithAccessLogOptions、worklog sweeper、TemplateStore 复用现有 ConfigCipher 与清理顺序；不得重复/遗漏 cleanup。
- [ ] 同步 README、开发指南、docker/.env.example、配置标签和旧注释；说明并发上限、异步日志降级、缓存语义、模板 Dify key 边界及 API key 加密/不回显口径。
- [ ] 运行 gofmt、`go test -count=1 ./...`、重点包 `go test -race`、`go vet ./...`、`go build ./...`；Web `npm run build`；小程序 JS/JSON 与格式化检查。
- [ ] `git status --short`、`git diff --check`、`git diff --name-only -- uvmp-toolkit`；删除临时文件，不删除用户已有文件。真实上游并发阈值、Dify 变量/API key 和微信真机明确列为待环境验收。

## Assumptions

- 单实例容器部署；进程内缓存、single-flight 和 keyed lock 不承诺多副本共享卷协调。
- 高并发以提高响应速度为目标，但不以上游风控、数据覆盖或日志自激为代价；生产若出现商旅 99000/99001 或 Dify 限流，应通过配置降低并发，而非恢复固定长 sleep。
- 安全日模板只配置章节回调 key、标题和各自的 Dify API key，不自定义生成表单字段，不改变 Dify 的 url/date/class 三个输入变量；不同提示词/输出键由对应 API key 所属的 Dify 工作流提供。
- bridge 微信通知保持顺序，quiz 已有 3 worker 不盲增；这两项不属于响应瓶颈。
