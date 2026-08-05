# 安全日活动记录生成平台

> **⚠️ 已废弃归档**：本服务已整合进主服务（`server/`）——后端合并为 `server/src/safeday/`（`/api/v1/safeday`），网页端重构为 `server/public/safeday.html`。本目录仅作历史归档，不再维护、不再部署。以下内容为历史文档。

上传学习文档（pdf/doc/docx/ppt/pptx/xls/xlsx）+ 填写日期，后端调用 Dify 工作流生成《安全日活动记录》，产物 `{date}.docx` 由 Dify 写入项目根目录的 `docs/` 文件夹（通过外部目录映射）。

## 环境要求

- Node.js ≥ 18

## 部署

```bash
cp .env.example .env
# 编辑 .env，至少填写 DIFY_BASE_URL 和 DIFY_API_KEY
npm install
npm run start
```

默认监听 3000 端口，浏览器访问 `http://服务器:3000`。

`.env` 配置项：

- `DIFY_BASE_URL`：Dify API 地址（如 `https://api.dify.ai` 或自建地址）
- `DIFY_API_KEY`：Dify 工作流 API Key
- `DIFY_USER`：传给 Dify 的 user 标识（默认 safeday-web）
- `PORT`：服务端口（默认 3000）
- `CALLBACK_TOKEN`：可选；设置后 `/api/callback` 必须带 `?token=` 匹配才生效

## Dify 侧配置

- 工作流入参：`document`（文件，type 为 document）、`date`（YYYY.MM.DD）、`name`（学习文件名称）。
- Dify 的产物目录需映射到本项目的 `docs/` 目录，产物文件名必须为 `{date}.docx`（如 `2025.07.29.docx`）。

### 完成回调（必需）

Dify 工作流运行中就会先创建**空的** `{date}.docx`，因此"文件存在"不代表完成，后端不会自行检测文件。判定规则：**只在收到 Dify 完成回调时检查一次**——文件存在即判定完成，不存在即判定失败（记录显示"生成失败"）。

必须在 Dify 工作流结束处（文件写完之后）加一个 HTTP 请求节点：

```
POST http://服务器:3000/api/callback?token=xxx
Content-Type: application/json

{"date":"2025.07.29"}
```

`token` 对应 `.env` 中的 `CALLBACK_TOKEN`；body 可为空（空则对所有"生成中"的记录做一次判定）。**未配置回调时，记录会一直停在"生成中"。**

## 文件说明

- 根目录 `safe.svg`：网站 favicon
- 根目录 `public-security.png`：公安备案图标
- `docs/`：Dify 产物目录（`{date}.docx`）
- `data/records.json`：生成记录存储
- `uploads/`：上传临时目录（预留）

## 上传限制

- 单文件最大 50MB，一次最多 10 个文件。
- 单文件支持 7 种格式：pdf/doc/docx/ppt/pptx/xls/xlsx。
- **多文件合并仅支持 PDF 格式**（纯 npm 方案，不依赖 LibreOffice）。

## MCP 服务（docx-mcp，供 Dify 调用）

`mcp/` 目录内置了 [docx-mcp](https://www.modelscope.cn/mcp/servers/rockcj/Docx_MCP)（42 个 Word 文档处理工具，Python/FastMCP）的 HTTP 部署封装。Dify 工作流可通过 MCP 协议调用它在 `docs/` 中创建/填充 `{date}.docx`，网站轮询检测到后即判定生成完成。

### 部署（Docker，推荐）

```bash
cd mcp
docker compose up -d --build
```

- 监听 `8000` 端口，传输协议 Streamable HTTP，端点路径 `/mcp/`
- **关键映射**：`../docs:/app/docs`（docker-compose.yml 中已配置），即网站项目的 `docs/` 就是 MCP 容器内的工作目录 `/app/docs`
- 也可在 1Panel 的「容器 → 编排」中导入 `mcp/docker-compose.yml` 运行

### Dify 侧配置

1. Dify → 工具 → MCP → 添加 MCP 服务，地址填：
   - Dify 与本服务同一台服务器：`http://172.17.0.1:8000/mcp/` 或 `http://<服务器内网IP>:8000/mcp/`
   - 若 Dify 版本较旧（<1.6，只支持 SSE）：把 docker-compose.yml 中 `MCP_TRANSPORT` 改为 `sse`、`MCP_PATH` 改为 `/sse/`，地址相应改为 `http://...:8000/sse`
2. 工作流中调用 MCP 工具时，**file_path 一律使用相对路径 `docs/{date}.docx`**（如 `docs/2025.07.29.docx`），文件即写入网站 `docs/` 目录。

### 安全注意

- MCP 端点**没有鉴权**，`8000` 端口不要对公网开放（安全组/防火墙只放行内网或本机）。
- 若 Dify 是外部 SaaS 必须公网访问，请在 1Panel 反向代理上为该端点加访问控制。
