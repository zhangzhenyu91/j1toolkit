// 任务终态 → 用户可读结果：状态、摘要、产物、排错日志展开策略。
// 供各业务页与执行历史共用，保持既有壳层行为一致。

const STATUS_TEXT = {
  success: '成功',
  failed: '失败',
  cancelled: '已取消',
  running: '执行中',
  pending: '排队中',
};

function hasResult(job) {
  return job && typeof job === 'object';
}

export function shouldExpandLog(outcome) {
  if (!outcome) return false;
  return outcome.failed === true || outcome.status === '失败' || outcome.status === '启动失败';
}

export function statusTheme(status) {
  switch (status) {
    case '成功':
    case '完成':
      return 'success';
    case '失败':
    case '启动失败':
      return 'danger';
    case '已暂停':
      return 'warning';
    case '已取消':
      return 'default';
    case '执行中':
      return 'primary';
    default:
      return 'default';
  }
}

export function friendlyTaskError(raw) {
  const text = String(raw || '');
  const lower = text.toLowerCase();
  if (lower.includes('sso') || text.includes('登录')) {
    return '派车系统登录失败，请检查账号和密码后重试；详情可查看排错日志。';
  }
  if (
    lower.includes('usb') ||
    text.includes('u盘') ||
    text.includes('U盘') ||
    text.includes('挂载')
  ) {
    return '未检测到可用 U 盘，请连接后重试；详情可查看排错日志。';
  }
  if (lower.includes('cdp') || lower.includes('playwright') || text.includes('浏览器')) {
    return '课程浏览器未就绪，请重新打开课程浏览器后再试。';
  }
  if (lower.includes('timeout') || lower.includes('deadline') || text.includes('超时')) {
    return '执行超时，请检查网络或稍后重试；详情可查看排错日志。';
  }
  if (lower.includes('certificate') || lower.includes('tls') || text.includes('证书')) {
    return '安全证书校验失败，请联系维护人员检查浏览器和证书配置。';
  }
  if (lower.includes('no such') || text.includes('不存在') || text.includes('路径')) {
    return '所需文件或目录不可用，请检查输入和保存位置。';
  }
  return '执行未完成，请检查输入后重试；仍失败时可展开排错日志联系技术支持。';
}

function baseOutcome(job, fallbackSummary = '') {
  const status = job?.result?.paused ? '已暂停' : STATUS_TEXT[job?.status] || job?.status || '';
  const failed = job?.status === 'failed' || (!!job?.error && !job?.result?.paused);
  let summary = '';
  if (job?.error) summary = friendlyTaskError(job.error);
  else if (failed && fallbackSummary) summary = fallbackSummary;
  return {
    status,
    summary,
    artifact: { label: '', path: '' },
    failed,
  };
}

function syncNightlySuffix(result) {
  return result?.nightly ? '（夜间兜底）' : '';
}

export function pcdOutcome(job) {
  const result = job?.result || {};
  const parts = [
    `共处理 ${result.total || 0} 单`,
    `派车单成功 ${result.pcd_ok || 0} / 失败 ${result.pcd_fail || 0}`,
    `轨迹成功 ${result.track_ok || 0} / 失败 ${result.track_fail || 0}`,
  ];
  if ((result.not_found || []).length) parts.push(`未找到 ${result.not_found.length} 单`);
  const failed = job?.status === 'failed' || (!!job?.error && !result.paused);
  const outcome = baseOutcome(job, failed ? '导出未完成：' + parts.join('；') : '');
  if (job?.status !== 'success' || job?.error) return outcome;
  outcome.summary = '导出完成：' + parts.join('；');
  if (result.dir) outcome.artifact = { label: '打开产物目录', path: result.dir };
  return outcome;
}

export function dailyOutcome(job) {
  const result = job?.result || {};
  const failed = job?.status === 'failed' || (!!job?.error && !result.paused);
  const failText = result.count
    ? `导出未完成：${result.count} 条派车单${result.file ? ' → ' + result.file : ''}`
    : '导出未完成：当日无派车数据';
  const outcome = baseOutcome(job, failed ? failText : '');
  if (job?.status !== 'success' || job?.error) return outcome;
  if (!result.count) {
    outcome.summary = '执行完成：当日无派车数据';
    return outcome;
  }
  const dest = result.dest ? `（写入 ${result.dest}）` : '';
  outcome.summary = `导出完成：${result.count} 条派车单 → ${result.file || ''}${dest}`;
  if (result.dir) outcome.artifact = { label: '打开产物目录', path: result.dir };
  return outcome;
}

export function syncOutcome(job, { usbMount = '', localPath = '' } = {}) {
  const result = job?.result || {};
  const kind = result.kind || result.action || '';
  const failed = job?.status === 'failed' || (!!job?.error && !result.paused);
  const failText = result.file
    ? `同步未完成：内网表 → ${result.file}`
    : '同步未完成，请展开排错日志查看详情。';
  const outcome = baseOutcome(job, failed ? failText : '');
  if (job?.status !== 'success' || job?.error) return outcome;
  const suffix = syncNightlySuffix(result);
  switch (result.skipped) {
    case 'usb_absent':
      outcome.status = '完成';
      outcome.summary = '本次未执行：U 盘未挂载（已跳过）' + suffix;
      return outcome;
    case 'no_command':
      outcome.status = '完成';
      outcome.summary = '本次未执行：U 盘无同步指令（已跳过）' + suffix;
      return outcome;
    case 'unknown_kind':
      outcome.status = '完成';
      outcome.summary = `本次未执行：未知指令 "${kind}"（已跳过）` + suffix;
      return outcome;
    case 'bad_file':
      outcome.status = '完成';
      outcome.summary = '本次未执行：指令中的文件名非法（已跳过）' + suffix;
      return outcome;
    default:
      break;
  }
  const file = result.file ? ` → ${result.file}` : '';
  if (kind === 'snapshot') {
    outcome.summary = '同步完成：内网表已拷贝到 U 盘' + file;
    if (usbMount) outcome.artifact = { label: '打开 U 盘目录', path: usbMount };
  } else if (kind === 'replace') {
    outcome.summary = '同步完成：U 盘新表已覆盖内网表（原文件备份为 .bak）' + file;
    if (localPath) {
      const sep = localPath.includes('\\') ? '\\' : '/';
      outcome.artifact = {
        label: '打开内网表目录',
        path: localPath.split(sep).slice(0, -1).join(sep) || localPath,
      };
    }
  } else {
    outcome.summary = '同步完成：' + (result.summary || '');
  }
  outcome.summary += suffix;
  return outcome;
}

export function courseOutcome(job, { scanOnly = false } = {}) {
  const outcome = baseOutcome(job);
  const result = job?.result || {};
  const counts = `总数 ${result.total ?? 0}；完成 ${result.completed ?? 0}；未完成 ${result.pending ?? 0}；跳过 ${result.skipped ?? 0}`;
  if (result.paused) {
    outcome.status = '已暂停';
    outcome.summary = '已暂停：浏览器与登录态保留，可稍后继续。' + counts;
    return outcome;
  }
  if (job?.status === 'success' && (result.pending ?? 0) > 0) {
    outcome.summary = '本轮结束，仍有未完成课程。' + counts;
    return outcome;
  }
  if (job?.status === 'success') {
    const label = scanOnly ? '扫描完成' : '学习完成（全部达到 100%）';
    outcome.summary = label + '：' + counts;
    if ((result.skipped ?? 0) > 0) {
      outcome.summary += `（跳过 ${result.skipped} 门）`;
    }
    return outcome;
  }
  if (job?.status === 'cancelled') {
    outcome.summary = '已取消：' + counts;
    return outcome;
  }
  if (!outcome.summary) outcome.summary = '执行失败：' + counts;
  return outcome;
}

export function formatHistorySummary(appID, result) {
  const value = result || {};
  switch (appID) {
    case 'pcd_export':
      return pcdOutcome({ status: 'success', result: value, error: '' }).summary;
    case 'daily_sync':
      if (!value.count) return '当日无派车数据';
      return `${value.count} 条 → ${value.file || ''}${value.dest ? '（' + value.dest + '）' : ''}`;
    case 'tree_sync':
    case 'crossing_sync': {
      const labels = {
        usb_absent: 'U盘未挂载，跳过',
        no_command: 'U 盘无同步指令，跳过',
        unknown_kind: `未知指令 ${value.kind || ''}（已跳过）`,
        bad_file: '指令 file 字段非法（已跳过）',
      };
      if (labels[value.skipped]) return labels[value.skipped];
      const action =
        { snapshot: '内网表 → U盘', replace: 'U盘新表 → 内网' }[value.kind || value.action] ||
        value.kind ||
        value.action ||
        '';
      return `${action}${value.nightly ? '（夜间兜底）' : ''}${value.cycle_id ? ' cycle=' + value.cycle_id : ''} → ${value.file || ''}`;
    }
    case 'course_autoplay':
      if (value.paused) return `已处理 ${value.processed ?? 0} 门次；可续刷`;
      return `总数 ${value.total ?? 0}；完成 ${value.completed ?? 0}；未完成 ${value.pending ?? 0}；跳过 ${value.skipped ?? 0}`;
    case 'quiz_search':
      return `题库搜题 ${value.count ? value.count + ' 题' : '已结束'}`;
    default:
      return value.summary || '';
  }
}

export function outcomeFromJob(appID, job, options = {}) {
  switch (appID) {
    case 'pcd_export':
      return pcdOutcome(job);
    case 'daily_sync':
      return dailyOutcome(job);
    case 'tree_sync':
    case 'crossing_sync':
      return syncOutcome(job, options);
    case 'course_autoplay':
      return courseOutcome(job, options);
    default:
      return baseOutcome(job);
  }
}

export { baseOutcome };
