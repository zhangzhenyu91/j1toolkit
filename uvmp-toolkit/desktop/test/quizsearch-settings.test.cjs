// 搜题设置：schema 钳制（坏配置不放大到窗口/轮询）。
const test = require('node:test');
const assert = require('node:assert/strict');

const { DEFAULT_SETTINGS, normalizeSettings } = require('../electron/quizsearch/settings.cjs');

const WORK_AREA = { x: 0, y: 0, width: 1920, height: 1040 };

test('缺省配置：返回默认值且不与常量共享引用', () => {
  const s = normalizeSettings(null, { workArea: WORK_AREA });
  assert.equal(s.interval, 1000);
  assert.equal(s.threshold, 0.6);
  assert.equal(s.opacity, 0.9);
  assert.deepEqual(s.boxes.A, { x: 80, y: 100, w: 720, h: 180, visible: true });
  assert.equal(s.result.w, 380);
  s.boxes.A.x = 1;
  assert.equal(DEFAULT_SETTINGS.boxes.A.x, 80);
});

test('越界与坏类型一律钳制到合法区间', () => {
  const s = normalizeSettings(
    {
      interval: 999999,
      threshold: '不是数字',
      opacity: 5,
      result: { x: 'x', y: null, w: -1, h: 'big' },
      boxes: {
        A: { x: 'left', y: undefined, w: -10, h: 999999, visible: 'yes' },
        B: { x: 3.7, y: -1, w: 1e9, h: 60.4, visible: 0 },
      },
    },
    { workArea: WORK_AREA }
  );
  assert.equal(s.interval, 3000);
  assert.equal(s.threshold, 0.6);
  assert.equal(s.opacity, 1);
  assert.equal(s.result.w, 260);
  assert.equal(s.result.h, 260);
  assert.equal(s.result.x, -1);
  assert.equal(s.result.y, -1);
  assert.equal(s.boxes.A.x, 80);
  assert.equal(s.boxes.A.y, 100);
  assert.equal(s.boxes.A.w, 160);
  assert.equal(s.boxes.A.h, 1040);
  assert.equal(s.boxes.A.visible, true);
  assert.equal(s.boxes.B.x, 4);
  assert.equal(s.boxes.B.h, 60);
  assert.equal(s.boxes.B.visible, false);
});

test('低于下限与小数间隔同样被钳制并对齐整数', () => {
  const s = normalizeSettings(
    { interval: 100, threshold: 0.1, opacity: 0 },
    { workArea: WORK_AREA }
  );
  assert.equal(s.interval, 500);
  assert.equal(s.threshold, 0.3);
  assert.equal(s.opacity, 0.3);
  const s2 = normalizeSettings({ interval: 1234.6 }, { workArea: WORK_AREA });
  assert.equal(s2.interval, 1235);
});

test('结果窗宽高不超工作区，B 框默认贴底但不越界', () => {
  const s = normalizeSettings(
    { result: { w: 5000, h: 5000 }, boxes: { B: { y: -1, h: 400 } } },
    { workArea: WORK_AREA }
  );
  assert.equal(s.result.w, 1920);
  assert.equal(s.result.h, 1040);
  assert.equal(s.boxes.B.y, WORK_AREA.y + WORK_AREA.height - 400 - 120);

  const small = normalizeSettings({}, { workArea: { x: 0, y: 0, width: 1280, height: 700 } });
  assert.equal(small.boxes.B.y, 700 - 180 - 120);
});

test('B 框只有默认哨兵 y=-1 才贴底；真实负坐标（副屏在上方）原样保留', () => {
  // 用户把 B 框拖到主屏上方的副屏：落盘 y 为负数，重启后必须回到原位而不是被拽回底部
  const above = normalizeSettings(
    { boxes: { B: { x: 120, y: -300, w: 700, h: 160 } } },
    {
      workArea: WORK_AREA,
    }
  );
  assert.equal(above.boxes.B.y, -300);
  assert.equal(above.boxes.B.x, 120);

  // 副屏工作区 y 为负（主屏在副屏下方）：贴底仍按所在工作区算
  const negWa = { x: 0, y: -1080, width: 1920, height: 1040 };
  const s = normalizeSettings({ boxes: { B: { y: -1, h: 200 } } }, { workArea: negWa });
  assert.equal(s.boxes.B.y, -1080 + 1040 - 200 - 120);

  // 非整数哨兵（旧版本可能写 -1.5 之类）不属于约定值，按真实坐标保留
  const odd = normalizeSettings({ boxes: { B: { y: -2 } } }, { workArea: WORK_AREA });
  assert.equal(odd.boxes.B.y, -2, '仅 -1 这一约定值触发贴底');

  // 完全没写过 B 框（首次运行）仍走默认贴底
  const fresh = normalizeSettings({ boxes: { B: { x: 80 } } }, { workArea: WORK_AREA });
  assert.equal(fresh.boxes.B.y, WORK_AREA.y + WORK_AREA.height - 180 - 120);
});

test('未知键不进入设置对象（防止脏配置落盘）', () => {
  const s = normalizeSettings({ hack: 1, boxes: { C: { x: 1 } } }, { workArea: WORK_AREA });
  assert.equal(s.hack, undefined);
  assert.equal(s.boxes.C, undefined);
  assert.deepEqual(Object.keys(s).sort(), ['boxes', 'interval', 'opacity', 'result', 'threshold']);
});
