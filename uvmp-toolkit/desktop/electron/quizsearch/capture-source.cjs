// 题库搜题：从 desktopCapturer 源列表里挑出目标显示器对应的那一张（纯函数）。
// 多显示器下 sources[0] 不保证是目标屏；盲取会截到别的显示器，表现为
// 「框里明明有题，答案窗一直未匹配」，且极难自查。宁可这一拍不截，也不能截错屏。
'use strict';

/**
 * @param sources desktopCapturer.getSources 结果
 * @param display 目标显示器（screen.getDisplayMatching 结果）
 * @param sourceCount 可用显示器总数（screen.getAllDisplays().length）
 * @returns {{source, reason}} source 为空时 reason 说明原因
 */
function pickSource(sources, display, sourceCount) {
  const list = Array.isArray(sources) ? sources : [];
  if (list.length === 0) return { source: null, reason: '未取到任何屏幕源' };
  const id = display && display.id;
  const hit = list.find(
    (s) => s && String(s.display_id) === String(id) && String(s.display_id) !== ''
  );
  if (hit) return { source: hit, reason: '' };
  // 仅一块显示器时只有一个源，回退无歧义
  if (sourceCount === 1 && list.length === 1) return { source: list[0], reason: '' };
  return {
    source: null,
    reason: '未能把屏幕源匹配到显示器 ' + (id === undefined ? '?' : id) + '（多显示器下不盲取）',
  };
}

module.exports = { pickSource };
