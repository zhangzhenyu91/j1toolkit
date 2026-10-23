// 题库刷题共用工具
// 接口 analysis = { none, pending, failed, done } 解析进度计数映射：index / bank / pool / manage
// 四页解析计数口径统一收敛于此；manage 页特有字段（anPct / anFinished / anRetryable）在其 mapBank 内基于本映射补充

// 解析进度计数归一 + 通用展示字段（计数全缺时用 fallbackTotal（questionCount）兜底进度分母）
function mapAnalysis(analysis, fallbackTotal) {
  const an = analysis || {};
  const anNone = an.none || 0;
  const anPending = an.pending || 0;
  const anFailed = an.failed || 0;
  const anDone = an.done || 0;
  const anTotal = anNone + anPending + anFailed + anDone;
  return {
    anNone,
    anPending,
    anFailed,
    anDone,
    anTotal,
    analyzing: anPending > 0,
    analyzingText: `解析中 ${anDone}/${anTotal || fallbackTotal || 0}`,
    // 解析就绪徽章：无排队且有已生成解析
    anReady: anPending === 0 && anDone > 0,
  };
}

module.exports = { mapAnalysis };
