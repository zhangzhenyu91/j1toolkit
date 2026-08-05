// 安全日活动记录：Dify 工作流封装（自 SafeDayLogs 独立服务移植）
// 地址用各工作流共用的 DIFY_API_URL（/v1 由代码拼接），本模块用独立的 DIFY_SAFEDAY_API_KEY
const config = require('../config');

const BASE_URL = () => (config.dify.apiUrl || '').replace(/\/+$/, '');
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
      // "succeeded" 不做完成标记，完成判定由文件检测负责
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
  try {
    reader.releaseLock();
  } catch (e) {
    /* ignore */
  }
}

/**
 * 上传文件到 Dify 并触发工作流。
 * 触发成功后立即返回，SSE 流在后台消费；
 * 工作流 failed/stopped 或流读取异常时调用 onFailed(error)。
 */
async function uploadAndRun({ fileBuffer, fileName, date, name, onFailed }) {
  const base = BASE_URL();
  if (!base || !API_KEY()) {
    throw new Error('未配置 DIFY_API_URL 或 DIFY_SAFEDAY_API_KEY');
  }

  // 1. 上传文件
  const form = new FormData();
  form.append('file', new Blob([fileBuffer]), fileName);
  form.append('user', USER());

  const uploadResp = await fetch(`${base}/v1/files/upload`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${API_KEY()}` },
    body: form,
  });
  if (!uploadResp.ok) {
    const text = await uploadResp.text().catch(() => '');
    throw new Error(`Dify 文件上传失败（HTTP ${uploadResp.status}）：${text.slice(0, 200)}`);
  }
  const uploadJson = await uploadResp.json();
  const upload_file_id = uploadJson.id;
  if (!upload_file_id) {
    throw new Error('Dify 文件上传响应缺少 id');
  }

  // 2. 触发工作流（streaming）
  const runResp = await fetch(`${base}/v1/workflows/run`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${API_KEY()}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      inputs: {
        document: {
          transfer_method: 'local_file',
          upload_file_id,
          type: 'document',
        },
        date,
        name,
      },
      response_mode: 'streaming',
      user: USER(),
    }),
  });
  if (!runResp.ok) {
    const text = await runResp.text().catch(() => '');
    throw new Error(`Dify 工作流触发失败（HTTP ${runResp.status}）：${text.slice(0, 200)}`);
  }

  // 3. 后台消费 SSE 流，catch 所有异常
  consumeStream(runResp, onFailed).catch((e) => {
    try {
      onFailed(`工作流流读取异常：${e && e.message ? e.message : e}`);
    } catch (err) {
      /* ignore */
    }
  });
}

module.exports = { uploadAndRun };
