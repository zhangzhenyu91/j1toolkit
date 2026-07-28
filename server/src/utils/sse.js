// SSE 解析器：兼容 WeKnora 的非标准格式（data: 后无空格），事件以 \n\n 分隔
// 详见《开发指南》第六章

/**
 * 创建 SSE 解析器
 * @param {function(Object):void} onEvent 每解析出一个 JSON 事件回调一次
 */
function createSseParser(onEvent) {
  let buffer = '';

  function handleEventStr(eventStr) {
    const lines = eventStr.split('\n');
    for (const line of lines) {
      if (!line.startsWith('data:')) continue;
      let jsonStr = line.substring(5);
      if (jsonStr.startsWith(' ')) jsonStr = jsonStr.substring(1); // 兼容 data: 与 data: 两种格式
      if (!jsonStr) continue;
      try {
        onEvent(JSON.parse(jsonStr));
      } catch (e) {
        // 忽略无法解析的行（如心跳注释等）
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
    // 流结束时冲刷残余 buffer
    end() {
      if (buffer.trim()) {
        const rest = buffer;
        buffer = '';
        handleEventStr(rest);
      }
    },
  };
}

module.exports = { createSseParser };
