// usb-agent 运行状态文案（树障/交跨/派车页共用）：
// 区分“后台监听没在跑”与“监听在跑但 U 盘未挂载/只读”，避免把两种失败混成一句「未检测到」。
function stamp(iso) {
  if (!iso) return '';
  return String(iso).replace('T', ' ').slice(0, 19);
}

export function agentStatusText(status) {
  const hb = (status && status.heartbeat) || {};
  if (!status || !status.running) {
    return {
      text: '未运行（同步指令不会被消费，请确认 uvmp-toolkit 服务/客户端在运行）',
      tone: 'danger',
    };
  }
  const when = stamp(hb.ts);
  const suffix = when ? `（${when} 检查）` : '（尚未上报心跳）';
  const reason = hb.last_error ? `：${hb.last_error}` : '';
  if (hb.mount) {
    // 已挂载但最近一轮仍有错误（只读文件系统、权限、指令执行失败）时亮警告，不冒充正常。
    return {
      text: `运行中 · ${hb.mount}${reason}${suffix}`,
      tone: hb.last_error ? 'warning' : 'success',
    };
  }
  return { text: `运行中 · U盘未挂载${reason}${suffix}`, tone: 'warning' };
}
