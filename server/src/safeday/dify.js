// 安全日活动记录：Dify 工作流封装（自 SafeDayLogs 独立服务移植）
// 2026-10-09 起改 URL 口径：学习文件（合并产物/单文件原样）先存 COS 拿公网地址，工作流入参为三个 string
// （url 学习文件地址 / date / class），不再经 /files/upload 上传 document 文件
// （实证：Dify 云端文档解析插件 files/upload 链路报 PluginRuntimeError no valid session response）
// 地址用各工作流共用的 DIFY_API_URL（/v1 由 utils/dify 拼接，配置已带 /v1 也不重复），本模块用独立的 DIFY_SAFEDAY_API_KEY
const config = require('../config');
const { apiBaseUrl, workflowRunUrl } = require('../utils/dify');

const API_KEY = () => config.safeday.difyKey || '';
const USER = () => 'safeday-web';

// 消费 Dify workflow 的 SSE 流（后台运行）
// response.body 是 web stream，用 getReader + TextDecoder 按行解析 "data: {json}"
async function consumeStream(response, onFailed) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  const handleLine = (line) => {
    const trimmed = line.trim();
    if (!trimmed || !trimmed.startsWith('data:')) return false;
    const payload = trimmed.slice('data:'.length).trim();
    if (!payload) return false;
    let evt;
    try {
      evt = JSON.parse(payload);
    } catch (e) {
      return false; // 非 JSON 行（如 ping），跳过
    }
    if (evt && evt.event === 'workflow_finished') {
      const status = evt.data && evt.data.status;
      if (status === 'failed' || status === 'stopped') {
        const errMsg =
          (evt.data && (evt.data.error || evt.data.message)) ||
          `Dify 工作流${status === 'stopped' ? '被停止' : '执行失败'}`;
        onFailed(String(errMsg));
      }
      // "succeeded" 不做完成标记：置 done 由 /callback 渲染落盘时置位，文件存在性终判仅是旧链路兼容
      return true; // 结束读取
    }
    return false;
  };

  let done = false;
  while (!done) {
    const { value, done: streamDone } = await reader.read();
    if (streamDone) break;
    buffer += decoder.decode(value, { stream: true });
    // 按行拆分，最后一段可能不完整，留在 buffer 里等下一个 chunk
    const lines = buffer.split(/\r?\n/);
    buffer = lines.pop();
    for (const line of lines) {
      if (handleLine(line)) {
        done = true;
        break;
      }
    }
  }
  // 冲刷残余
  buffer += decoder.decode();
  if (!done && buffer) {
    handleLine(buffer);
  }
  // workflow_finished 后主动取消底层流，释放连接（releaseLock 不含取消，流未读完会挂着）
  try {
    await reader.cancel();
  } catch (e) {
    /* ignore */
  }
  try {
    reader.releaseLock();
  } catch (e) {
    /* ignore */
  }
}

/**
 * 触发 Dify 工作流（入参三个 string：url 学习文件 COS 公网地址 / date YYYY.MM.DD / class 班组名）。
 * 触发成功后立即返回，SSE 流在后台消费；
 * 工作流 failed/stopped 或流读取异常时调用 onFailed(error)。
 * 工作流末尾由 HTTP 节点把三段文字 + date + class 回传 /callback，后端据此渲染 docx 落盘（不再由工作流写文件）
 */
async function runWorkflow({ url, date, className, onFailed }) {
  if (!apiBaseUrl(config.dify.apiUrl) || !API_KEY()) {
    throw new Error('未配置 DIFY_API_URL 或 DIFY_SAFEDAY_API_KEY');
  }

  const runResp = await fetch(workflowRunUrl(config.dify.apiUrl), {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${API_KEY()}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      inputs: { url, date, class: className || '' },
      response_mode: 'streaming',
      user: USER(),
    }),
  });
  if (!runResp.ok) {
    const text = await runResp.text().catch(() => '');
    throw new Error(`Dify 工作流触发失败（HTTP ${runResp.status}）：${text.slice(0, 200)}`);
  }

  // 后台消费 SSE 流，catch 所有异常
  consumeStream(runResp, onFailed).catch((e) => {
    try {
      onFailed(`工作流流读取异常：${e && e.message ? e.message : e}`);
    } catch (err) {
      /* ignore */
    }
  });
}

module.exports = { runWorkflow };
