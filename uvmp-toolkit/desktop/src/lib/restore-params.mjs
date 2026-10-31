// 历史参数快照 → 页面表单值：只接受类型正确的字段，不自动再次执行。
function restoreBool(value, fallback) {
  return typeof value === 'boolean' ? value : fallback;
}

function restorePositiveInt(value, fallback) {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? Math.round(number) : fallback;
}

export function restorePcdParams(params = {}) {
  return {
    outdir: typeof params.outdir === 'string' ? params.outdir : '',
    batchName: typeof params.batch_name === 'string' ? params.batch_name : '派车单',
    options: {
      batchSubdir: restoreBool(params.batch_subdir, true),
      pcd: restoreBool(params.export_pcd, true),
      track: restoreBool(params.export_track, true),
      calibrateNoon: restoreBool(params.calibrate_noon, false),
    },
    codes: Array.isArray(params.codes) ? params.codes : [],
    codesText: typeof params.codes_text === 'string' ? params.codes_text : '',
    xlsxPath: typeof params.xlsx_path === 'string' ? params.xlsx_path : '',
  };
}

export function restoreDailyParams(params = {}) {
  return { date: typeof params.date === 'string' ? params.date : '' };
}

export function restoreSyncParams(params = {}) {
  return { action: params.nightly ? 'nightly' : 'tick' };
}

export function restoreCourseParams(params = {}) {
  return {
    scanOnly: restoreBool(params.scan_only, false),
    timeout: restorePositiveInt(params.timeout, 120),
    maxAttempts: restorePositiveInt(params.max_attempts, 3),
  };
}
