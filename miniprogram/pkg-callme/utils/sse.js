// 小程序端 SSE 工具：ArrayBuffer 转字符串 + 兼容 data:/data: 两种格式的解析器
// 对接细节见《开发指南》第六章

// ArrayBuffer → 字符串（含中文 UTF-8 处理）
function arrayBufferToString(buffer) {
  const array = new Uint8Array(buffer);
  let str = '';
  for (let i = 0; i < array.length; i += 1) {
    str += String.fromCharCode(array[i]);
  }
  try {
    return decodeURIComponent(escape(str));
  } catch (e) {
    return str;
  }
}

// 与服务端 utils/sse.js 同构的解析器：事件以 \n\n 分隔，忽略非 data 行（如心跳注释）
function createSseParser(onEvent) {
  let buffer = '';

  function handleEventStr(eventStr) {
    const lines = eventStr.split('\n');
    for (const line of lines) {
      if (!line.startsWith('data:')) continue;
      let jsonStr = line.substring(5);
      if (jsonStr.startsWith(' ')) jsonStr = jsonStr.substring(1);
      if (!jsonStr) continue;
      try {
        onEvent(JSON.parse(jsonStr));
      } catch (e) {
        // 忽略无法解析的行
      }
    }
  }

  return {
    push(chunkStr) {
      buffer += chunkStr;
      const events = buffer.split('\n\n');
      buffer = events.pop() || '';
      for (const eventStr of events) {
        handleEventStr(eventStr);
      }
    },
    end() {
      if (buffer.trim()) {
        const rest = buffer;
        buffer = '';
        handleEventStr(rest);
      }
    },
  };
}

module.exports = { arrayBufferToString, createSseParser };
