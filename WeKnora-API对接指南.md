# WeKnora API 对接指南

> 基于实际项目开发经验总结，避免踩坑。

---

## 一、基础信息

| 项目 | 值 |
|------|-----|
| API地址 | `https://know.j1net.com/api/v1` |
| 认证方式 | `X-API-Key` 请求头 |
| 响应格式 | JSON / SSE (text/event-stream) |
| 会话模式 | 需先创建session，再使用session_id进行对话 |

---

## 二、认证方式

**所有API请求**都需要在请求头中携带API Key：

```http
X-API-Key: sk-xxxxxxxxxxxxxxxxxxxx
```

> **注意**：不是 `Authorization: Bearer xxx`，是 `X-API-Key`。

### 2.1 终端用户标识（X-External-User-ID）

会话按终端用户隔离，**所有会话与对话接口**还需携带：

```http
X-External-User-ID: user_123
```

- 取值由调用方自定（建议直接用本系统的**登录账号**，如 `zhangzhenyu91`，不带任何前缀）；同一用户的会话列表、历史记录、对话上下文均按此标识隔离；
- **不传该头的坑**：历史记录/会话列表可能取不回（且所有调用方共享同一批会话，互相可见）；
- `POST /agent-chat/{session_id}` 的 payload 建议同时携带 `"channel": "api"`。

---

## 三、核心流程

### 3.1 创建会话（获取session_id）

```http
POST /api/v1/sessions
Content-Type: application/json
X-API-Key: sk-xxx

{
  "title": "对话标题",
  "description": "对话描述（可选）"
}
```

**响应：**
```json
{
  "success": true,
  "data": {
    "id": "411d6b70-9a85-4d03-bb74-aab0fd8bd12f",
    "title": "对话标题",
    "tenant_id": 1,
    "is_pinned": false,
    "created_at": "2026-03-27T12:26:19+08:00",
    "updated_at": "2026-03-27T12:26:19+08:00"
  }
}
```

> **关键**：`data.id` 就是 `session_id`，后续所有对话操作都需要用到。

### 3.2 发送消息（Agent对话）

```http
POST /api/v1/agent-chat/{session_id}
Content-Type: application/json
X-API-Key: sk-xxx

{
  "query": "用户的问题",
  "agent_id": "36a0b984-8571-416c-82d0-95a201d7f8a6",
  "agent_enabled": true,
  "web_search_enabled": false,
  "enable_memory": true,
  "images": [
    { "data": "data:image/png;base64,iVBOR..." }
  ]
}
```

**请求参数说明：**

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `query` | string | 是 | 用户问题文本 |
| `agent_id` | string | 是 | 智能体ID |
| `agent_enabled` | bool | 否 | 启用Agent模式（默认false） |
| `web_search_enabled` | bool | 否 | 启用网络搜索（默认false） |
| `enable_memory` | bool | 否 | 启用上下文记忆（默认false） |
| `images` | object[] | 否 | 图片数组，格式 `[{ "data": "data:image/png;base64,..." }]` |
| `knowledge_base_ids` | string[] | 否 | 指定知识库ID列表 |

### 3.3 其他会话管理接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/sessions` | 获取会话列表 |
| GET | `/sessions/{id}` | 获取会话详情（**仅元数据，不含消息**） |
| GET | `/messages/{session_id}/load` | **获取会话历史消息**（`before_time`/`limit` 分页，默认 20 条） |
| PUT | `/sessions/{id}` | 更新会话（标题等） |
| DELETE | `/sessions/{id}` | 删除会话 |
| DELETE | `/messages/{session_id}/{id}` | 删除单条消息（旧文档的 `DELETE /sessions/{id}/messages` 已失效） |
| POST | `/sessions/{id}/generate_title` | 自动生成会话标题 |

---

## 四、SSE流式响应（重点难点）

### 4.1 响应格式

响应头：
```
Content-Type: text/event-stream
```

**SSE事件格式（关键！）：**
```
event:message
data:{"id":"xxx","response_type":"answer","content":"你好","done":false}

event:message
data:{"id":"xxx","response_type":"answer","content":"！有什么","done":false}

event:message
data:{"id":"xxx","response_type":"answer","content":"","done":true}
```

### 4.2 踩坑点

> **重要**：`data:` 后面**没有空格**！是 `data:{json}` 不是 `data: {json}`

标准SSE格式是 `data: {json}`（有空格），但WeKnora返回的是 `data:{json}`（无空格）。

解析代码必须兼容两种格式：
```javascript
// 兼容 data: 和 data: 两种格式
let jsonStr = null;
if (line.startsWith('data:')) {
  jsonStr = line.substring(5);
  if (jsonStr.startsWith(' ')) jsonStr = jsonStr.substring(1);
}
```

### 4.3 事件分隔

SSE事件以 `\n\n`（双换行/空行）分隔：
```javascript
const events = rawString.split('\n\n');
```

### 4.4 响应类型（response_type）

| response_type | 说明 | content |
|---------------|------|---------|
| `agent_query` | Agent开始处理 | 通常为空 |
| `thinking` | Agent思考过程 | 思考文本 |
| `tool_call` | 工具调用 | 调用信息 |
| `tool_result` | 工具结果 | 结果文本 |
| `references` | 知识库引用 | 通常为空，引用在 `knowledge_references` 字段 |
| `answer` | 最终回答 | 回答内容（增量） |
| `error` | 错误 | 错误信息 |

### 4.5 完整SSE解析示例（Node.js后端）

```javascript
const axios = require('axios');

const response = await axios.post(
  `https://know.j1net.com/api/v1/agent-chat/${sessionId}`,
  payload,
  {
    headers: { 'X-API-Key': API_KEY, 'Content-Type': 'application/json' },
    responseType: 'stream',
    timeout: 120000
  }
);

let aiContent = '';
let buffer = '';

response.data.on('data', (chunk) => {
  buffer += chunk.toString();
  
  // SSE事件以\n\n分隔
  const events = buffer.split('\n\n');
  buffer = events.pop() || '';
  
  for (const eventStr of events) {
    const lines = eventStr.split('\n');
    for (const line of lines) {
      // 兼容 data: 和 data: 两种格式
      let jsonStr = null;
      if (line.startsWith('data:')) {
        jsonStr = line.substring(5);
        if (jsonStr.startsWith(' ')) jsonStr = jsonStr.substring(1);
      }
      
      if (jsonStr) {
        try {
          const data = JSON.parse(jsonStr);
          
          switch (data.response_type) {
            case 'answer':
              if (data.content) {
                aiContent += data.content;
                // 实时推送给前端
              }
              if (data.done) {
                console.log('回答完成');
              }
              break;
            case 'thinking':
              // 处理思考过程
              break;
            case 'tool_call':
              // 处理工具调用
              break;
            // ...
          }
        } catch (e) {
          // 忽略解析错误
        }
      }
    }
  }
});

response.data.on('end', () => {
  // 处理剩余buffer（同上逻辑）
  // 保存完整回复到数据库
});

response.data.on('error', (error) => {
  console.error('流错误:', error);
});
```

### 4.6 转发SSE给小程序

如果后端需要将SSE转发给小程序：

```javascript
// 设置SSE响应头
res.setHeader('Content-Type', 'text/event-stream');
res.setHeader('Cache-Control', 'no-cache, no-transform');
res.setHeader('Connection', 'keep-alive');
res.setHeader('X-Accel-Buffering', 'no'); // 禁用Nginx缓冲
res.flushHeaders(); // 立即发送响应头

// 发送SSE数据
const sendSSE = (data) => {
  res.write(`data: ${JSON.stringify(data)}\n\n`);
};

// 流结束时
sendSSE({ type: 'done', content: aiContent });
res.end();
```

---

## 五、小程序端对接

### 5.1 启用分块传输

```javascript
const requestTask = wx.request({
  url: apiUrl,
  method: 'POST',
  header: { 'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json' },
  data: payload,
  enableChunked: true,  // 关键：启用分块传输
  responseType: 'text',
  success: (res) => { /* 请求完成 */ },
  fail: (error) => { /* 请求失败 */ }
});
```

### 5.2 监听分块数据

```javascript
if (requestTask && requestTask.onChunkReceived) {
  let buffer = '';
  
  requestTask.onChunkReceived((res) => {
    const text = arrayBufferToString(res.data);
    buffer += text;
    
    // 同样按\n\n分隔，同样兼容 data: 和 data:
    const events = buffer.split('\n\n');
    buffer = events.pop() || '';
    
    for (const eventStr of events) {
      const lines = eventStr.split('\n');
      for (const line of lines) {
        let jsonStr = null;
        if (line.startsWith('data:')) {
          jsonStr = line.substring(5);
          if (jsonStr.startsWith(' ')) jsonStr = jsonStr.substring(1);
        }
        if (jsonStr) {
          try {
            const data = JSON.parse(jsonStr);
            // 处理SSE数据，更新UI
          } catch (e) {}
        }
      }
    }
  });
}
```

### 5.3 ArrayBuffer转字符串

```javascript
function arrayBufferToString(buffer) {
  const array = new Uint8Array(buffer);
  let str = '';
  for (let i = 0; i < array.length; i++) {
    str += String.fromCharCode(array[i]);
  }
  try {
    return decodeURIComponent(escape(str));
  } catch (e) {
    return str;
  }
}
```

---

## 六、图片发送

图片直接以base64编码发送，**不需要上传到COS等存储服务**：

```javascript
// 小程序端：图片转base64
wx.getFileSystemManager().readFile({
  filePath: tempFilePath,
  encoding: 'base64',
  success: (res) => {
    const base64 = `data:image/png;base64,${res.data}`;
    // 发送到后端
  }
});

// 后端：传递给WeKnora
const payload = {
  query: '请分析图片',
  agent_id: agentId,
  agent_enabled: true,
  images: [{ data: 'data:image/png;base64,iVBOR...' }]
};
```

---

## 七、Nginx反向代理注意事项

如果后端通过Nginx反向代理，SSE流式传输需要：

```nginx
location /api/ {
    proxy_pass http://127.0.0.1:8003;
    proxy_buffering off;           # 禁用缓冲
    proxy_cache off;               # 禁用缓存
    chunked_transfer_encoding on;  # 启用分块传输
    proxy_read_timeout 120s;       # 超时时间
}
```

---

## 八、常见问题

| 问题 | 原因 | 解决 |
|------|------|------|
| 401 Unauthorized | 认证头错误 | 使用 `X-API-Key` 而非 `Authorization` |
| 404 Not Found | session_id不存在 | 先调用 `POST /sessions` 创建会话 |
| SSE数据不解析 | `data:` 格式问题 | 兼容 `data:` 和 `data: ` 两种格式 |
| 流式响应不实时 | Nginx缓冲 | 设置 `proxy_buffering off` |
| 小程序不显示内容 | `enableChunked` 未启用 | 设置 `enableChunked: true` |
| `onChunkReceived` 不触发 | 环境不支持 | 添加 `success` 回调兜底处理 |

---

## 九、完整对接流程图

```
[创建会话] POST /sessions → 获取 session_id
      ↓
[发送消息] POST /agent-chat/{session_id}
      ↓
[接收SSE流] data:{json} 按\n\n分隔
      ↓
[解析事件] response_type: thinking → 显示思考过程
           response_type: answer   → 拼接回答内容
           response_type: done     → 完成，保存回复
      ↓
[保存到DB] INSERT INTO messages
```
