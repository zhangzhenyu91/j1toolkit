// 出工日志纯逻辑：未出车判定、核验清单行、键盘编辑键与删除照片确认文案。
// 视图与组件只消费本文件，node:test 直接导入，不加载 SFC。

// isNoCar 未出车（站内作业）：服务端统一以 vehicle_id=0 表示未出车，
// 历史/兼容数据可能为 null，两者都必须按未出车处理（不参与验证）。
export function isNoCar(vehicleId) {
  return vehicleId == null || Number(vehicleId) === 0;
}

// reportRows 汇总前核验清单行：服务端已按口径（未通过 ∪ 有备注；scope=mine 时为「仅我」原因）
// 过滤，前端不得再按 verify==='failed' 二次筛选，否则会丢掉个人原因行与有备注行。
export function reportRows(list) {
  return Array.isArray(list) ? list.filter(Boolean) : [];
}

// reportRowChips 单行原因标签：优先服务端 reasons；无原因时仅在确有备注时标「有备注」，
// 整体通过且无原因/备注返回空数组（不得谎报有备注）；未通过无原因才标「未通过」。
export function reportRowChips(item) {
  const it = item || {};
  const reasons = Array.isArray(it.reasons) ? it.reasons.filter(Boolean) : [];
  if (reasons.length) return reasons;
  if (it.remark || it.remark_has_files) return ['有备注'];
  if (it.verify === 'failed') return ['未通过'];
  return [];
}

// isActivationKey 键盘激活键（Enter / Space），用于卡片编辑入口的可访问键盘操作。
export function isActivationKey(key) {
  return key === 'Enter' || key === ' ' || key === 'Spacebar';
}

// deletePhotoConfirmText 删除照片二次确认文案（带所属人，便于确认是否删错）。
export function deletePhotoConfirmText(photo) {
  const members = (photo && Array.isArray(photo.members) ? photo.members : []).filter(Boolean);
  if (members.length) return `确定删除这张照片（${members.join('、')}）？删除后不可恢复。`;
  return '确定删除这张照片？删除后不可恢复。';
}

// photoOpFeedback 照片异步任务反馈：删除与重新同步分别展示进行中、成功、失败与查询中断结果。
export function photoOpFeedback(kind) {
  if (kind === 'delete') {
    return {
      text: '解除商旅关联中',
      success: '已删除',
      failure: '删除失败，请重试',
      timeout: '任务状态查询中断，请刷新确认是否已删除',
    };
  }
  return {
    text: '商旅重新同步中',
    success: '同步成功',
    failure: '商旅同步失败，请重试',
    timeout: '任务状态查询中断，请刷新确认同步结果',
  };
}

// photoOpPhase 照片任务轮询阶段判定：unknown（任务不存在、过期或无权查看）必须先于
// done 识别——服务端此时不代表成功，端侧应停止轮询、摘锁刷新并提示待确认；
// failed 次之，其余视为进行中。
export function photoOpPhase(st) {
  if (!st) return 'running';
  if (st.step === 'unknown' || st.status === 'unknown') return 'unknown';
  if (st.step === 'done') return 'done';
  if (st.failed || st.step === 'failed') return 'failed';
  return 'running';
}

// photoOpStart 照片任务开始：返回任务锁与轮询入参（opId/entryId/photoId/kind）。
// 照片 op 与卡片级同步是两套后端任务，同卡片已有任务锁（从商旅同步 / 旁观者锁）时
// 也必须照常轮询收尾；调用方若因已有锁丢弃轮询，删除会已发起却无终态提示、列表也不刷新。
// 锁内保留 opId 供轮询只收口自己的任务，photoId 供照片抽屉定位到具体照片行。
export function photoOpStart({ opId, entryId, photoId, kind = 'resync' } = {}) {
  if (!opId || !entryId) return null;
  const lock = { opId, pct: 0, text: photoOpFeedback(kind).text };
  if (photoId) lock.photoId = photoId;
  return {
    entryId,
    opId,
    kind,
    lock,
  };
}

// entryAfterRefresh 照片管理抽屉的 entry 更新口径：日志刷新后按 id 取新列表对象，
// 删除任务收尾后抽屉不得继续渲染旧对象里的已删除照片；卡片已不在列表则返回 null。
export function entryAfterRefresh(list, entry) {
  if (!entry || entry.id == null) return null;
  return (Array.isArray(list) ? list : []).find((it) => it && it.id === entry.id) || null;
}
