// 文件传输终态收口：上传/批量下载/取消/设备切换都必须清空同一组互斥进行态，
// 避免异常分支留下 uploading/downloading 永久为 true。
function transferTerminalPatch(extra) {
  return Object.assign(
    {
      uploading: false,
      dlBusy: false,
      dlLoading: false,
      downloading: '',
      mounting: false,
      savingNd: '',
    },
    extra || {}
  );
}

// 设备 /status 查询失败：U 盘挂载状态未知，只能提示用户显式确认后再取回（禁止自动调 /files，
// 该接口会断开共享并把 U 盘挂载回 KVM）。
function statusFailureRequiresConfirm() {
  return true;
}

// 状态查询失败时的动作：401 已由统一收口跳登录，不再弹确认；其余进入可重试错误态。
function statusFailureAction(err) {
  return err && err.statusCode === 401 ? 'ignore' : 'error';
}

// 取消确认后的可重试错误态（保留弹层，由用户点重试）。
function statusErrorPatch(err) {
  return { dlState: 'error', dlError: (err && err.message) || '读取状态失败，请重试' };
}

// 确认执行 /files（有断开共享副作用）前复核：请求代际仍当前、弹层仍开、设备未切换、未 401。
function canConfirmStatusLoad(input) {
  const s = input || {};
  return !!(s.ticketCurrent && s.dlOpen && s.sameDevice && !s.unauthorized);
}

// 传输进行中禁止切换设备/关闭弹层（否则旧任务会落到新设备或新状态上）
function deviceSwitchBlocked(state) {
  if (!state) return false;
  return !!(state.uploading || state.dlBusy || state.downloading || state.mounting);
}

module.exports = {
  transferTerminalPatch,
  statusFailureRequiresConfirm,
  statusFailureAction,
  statusErrorPatch,
  canConfirmStatusLoad,
  deviceSwitchBlocked,
};
