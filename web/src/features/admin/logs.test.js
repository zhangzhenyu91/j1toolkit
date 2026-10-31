import test from 'node:test';
import assert from 'node:assert/strict';

import {
  LOG_DEFAULT_RANGE_HOURS,
  LOG_PENDING_ALERT_THRESHOLD,
  LOG_VIEW_OPTIONS,
  LOG_CATEGORY_OPTIONS,
  LOG_RANGE_OPTIONS,
  normalizeLogView,
  categoryName,
  levelName,
  levelClass,
  rangeSince,
  formatLogTime,
  serializeLogQuery,
  keysetFromItems,
  mergeLogItems,
  isTotalKnown,
  healthAlerts,
  healthSeverity,
  logEventTime,
  logTotalText,
  syncViewForCategory,
  syncCategoryForView,
  exportWarning,
  exportStartedMessage,
  normalizeExportTicket,
  triggerBrowserDownload,
  startLogExport,
} from './logs.js';

test('视图选项与默认值：默认 activity，保留全部/访问', () => {
  assert.deepEqual(
    LOG_VIEW_OPTIONS.map((o) => o.value),
    ['activity', 'access', 'all']
  );
  assert.equal(normalizeLogView(undefined), 'activity');
  assert.equal(normalizeLogView(''), 'activity');
  assert.equal(normalizeLogView('ACCESS'), 'access');
  assert.equal(normalizeLogView('all'), 'all');
  assert.equal(normalizeLogView('bogus'), 'activity');
});

test('类别映射补齐 access/admin/audit/runtime/security', () => {
  const expected = {
    business: '业务事件',
    external: '外部调用',
    scheduler: '定时任务',
    config: '配置变更',
    message: '微信消息',
    access: '访问日志',
    admin: '管理操作',
    audit: '写操作审计',
    runtime: '运行错误',
    security: '安全事件',
  };
  for (const [value, label] of Object.entries(expected)) {
    assert.equal(categoryName(value), label, value);
  }
  assert.equal(categoryName(''), '—');
  assert.equal(categoryName('custom'), 'custom');
  const values = LOG_CATEGORY_OPTIONS.map((o) => o.value);
  for (const value of Object.keys(expected)) {
    assert.ok(values.includes(value), `类别选项缺少 ${value}`);
  }
});

test('级别名称与状态色', () => {
  assert.equal(levelName('info'), '信息');
  assert.equal(levelName('warn'), '警告');
  assert.equal(levelName('error'), '错误');
  assert.equal(levelName('debug'), '调试');
  assert.equal(levelName('other'), 'other');
  assert.equal(levelClass('error'), 'red');
  assert.equal(levelClass('warn'), 'orange');
  assert.equal(levelClass('info'), 'blue');
  assert.equal(levelClass('debug'), '');
});

test('时间范围：默认 7 天仅为查询窗口，支持 30/90/365 天与全部', () => {
  assert.equal(LOG_DEFAULT_RANGE_HOURS, 168);
  const values = LOG_RANGE_OPTIONS.map((o) => o.value);
  for (const value of [24, 72, 168, 720, 2160, 8760, 0]) {
    assert.ok(values.includes(value), `时间范围缺少 ${value} 小时`);
  }
  const now = Date.parse('2026-10-31T12:00:00Z');
  assert.equal(rangeSince(168, now), '2026-10-24T12:00:00.000Z');
  assert.equal(rangeSince(0, now), '');
});

test('UTC 时间按本地/指定时区显示，不再直接删 Z', () => {
  assert.equal(
    formatLogTime('2026-10-30T16:05:09Z', { timeZone: 'Asia/Shanghai' }),
    '2026-10-31 00:05:09'
  );
  assert.equal(
    formatLogTime('2026-10-30T08:00:00+08:00', { timeZone: 'Asia/Shanghai' }),
    '2026-10-30 08:00:00'
  );
  assert.equal(
    formatLogTime(new Date('2026-10-30T08:00:00Z'), { timeZone: 'UTC' }),
    '2026-10-30 08:00:00'
  );
  assert.equal(formatLogTime(''), '—');
  assert.equal(formatLogTime('not-a-date'), '—');
});

test('occurred_at 零值不得遮住 created_at', () => {
  assert.equal(
    logEventTime({
      occurred_at: '0001-01-01T00:00:00Z',
      created_at: '2026-10-30T08:00:00Z',
    }),
    '2026-10-30T08:00:00Z'
  );
  assert.equal(
    logEventTime({
      occurred_at: '2026-10-30T09:00:00Z',
      created_at: '2026-10-30T08:00:00Z',
    }),
    '2026-10-30T09:00:00Z'
  );
  assert.equal(logEventTime({}), '');
  assert.equal(logEventTime(null), '');
});

test('视图与类别联动，避免 access 视图选非 access 类别空结果', () => {
  assert.equal(syncViewForCategory('activity', 'access'), 'access');
  assert.equal(syncViewForCategory('access', 'access'), 'access');
  assert.equal(syncViewForCategory('access', 'business'), 'activity');
  assert.equal(syncViewForCategory('access', ''), 'access');
  assert.equal(syncViewForCategory('all', 'access'), 'all');
  assert.equal(syncViewForCategory('activity', 'business'), 'activity');
  assert.equal(syncCategoryForView('access', 'business'), '');
  assert.equal(syncCategoryForView('access', 'access'), 'access');
  assert.equal(syncCategoryForView('access', ''), '');
  assert.equal(syncCategoryForView('activity', 'access'), '');
  assert.equal(syncCategoryForView('all', 'access'), 'access');
});

test('totalText 在 total=-1 时不误导', () => {
  assert.equal(logTotalText(42, 20, true), '共 42 条，已加载 20 条');
  assert.equal(logTotalText(20, 20, false), '共 20 条，已加载 20 条');
  assert.equal(logTotalText(-1, 20, true), '已加载 20 条，仍有更早日志（未统计总数）');
  assert.equal(logTotalText(-1, 3, false), '已加载全部 3 条（未统计总数）');
  assert.equal(logTotalText(-1, 0, false), '已加载全部 0 条（未统计总数）');
});

test('筛选序列化：view/时间/键集/空值省略', () => {
  assert.equal(serializeLogQuery({}), 'view=activity&page=1&page_size=20');

  const full = serializeLogQuery({
    view: 'access',
    appCode: 'work-log',
    level: 'warn',
    category: 'access',
    keyword: '  照片  ',
    since: new Date('2026-10-24T12:00:00Z'),
    until: '2026-10-31T12:00:00+08:00',
    page: 2,
    pageSize: 50,
  });
  const p = new URLSearchParams(full);
  assert.equal(p.get('view'), 'access');
  assert.equal(p.get('app_code'), 'work-log');
  assert.equal(p.get('level'), 'warn');
  assert.equal(p.get('category'), 'access');
  assert.equal(p.get('keyword'), '照片');
  assert.equal(p.get('since'), '2026-10-24T12:00:00.000Z');
  assert.equal(p.get('until'), '2026-10-31T12:00:00+08:00');
  assert.equal(p.get('page'), '2');
  assert.equal(p.get('page_size'), '50');

  const keyset = new URLSearchParams(
    serializeLogQuery({ view: 'all', beforeTime: '2026-10-30T08:00:00Z', beforeId: 42 })
  );
  assert.equal(keyset.get('view'), 'all');
  assert.equal(keyset.get('before_time'), '2026-10-30T08:00:00Z');
  assert.equal(keyset.get('before_id'), '42');
  assert.equal(keyset.has('keyword'), false);
});

test('键集游标与列表合并去重', () => {
  assert.equal(keysetFromItems([]), null);
  assert.equal(keysetFromItems([{ id: 8, occurred_at: '' }]), null);
  assert.deepEqual(
    keysetFromItems([
      { id: 9, occurred_at: '2026-10-30T08:00:00Z' },
      { id: 8, occurred_at: '2026-10-30T07:00:00Z' },
    ]),
    { beforeTime: '2026-10-30T07:00:00Z', beforeId: 8 }
  );
  assert.deepEqual(
    mergeLogItems([{ id: 3 }, { id: 2 }], [{ id: 2 }, { id: 1 }]).map((i) => i.id),
    [3, 2, 1]
  );
  assert.equal(isTotalKnown(-1), false);
  assert.equal(isTotalKnown(null), false);
  assert.equal(isTotalKnown(0), true);
  assert.equal(isTotalKnown(42), true);
});

test('健康告警：磁盘/入库/积压/数据库未接入分级输出', () => {
  assert.equal(LOG_PENDING_ALERT_THRESHOLD, 10000);
  assert.deepEqual(
    healthAlerts({
      db_connected: true,
      pending: 0,
      write_errors: 0,
      disk_fallbacks: 0,
      insert_errors: 0,
    }),
    []
  );

  const alerts = healthAlerts({
    db_connected: false,
    pending: 12000,
    write_errors: 2,
    disk_fallbacks: 1,
    insert_errors: 3,
  });
  assert.ok(alerts.some((a) => a.level === 'error' && a.text.includes('磁盘')));
  assert.ok(alerts.some((a) => a.level === 'error' && a.text.includes('入库失败')));
  assert.ok(alerts.some((a) => a.level === 'warn' && a.text.includes('待补录')));
  assert.ok(alerts.some((a) => a.text.includes('数据库')));
  assert.equal(healthSeverity({ db_connected: false }), 'warn');
  assert.equal(
    healthSeverity({
      db_connected: true,
      pending: 12000,
      write_errors: 0,
      disk_fallbacks: 0,
      insert_errors: 0,
    }),
    'warn'
  );
  assert.equal(
    healthSeverity({
      db_connected: true,
      pending: 0,
      write_errors: 1,
      disk_fallbacks: 0,
      insert_errors: 0,
    }),
    'error'
  );
  assert.equal(healthSeverity({ db_connected: true, pending: 0 }), 'ok');
  assert.equal(healthSeverity(null), 'ok');
});

test('磁盘回退时导出提示文件副本可能不完整（health 或 ticket 任一来源）', () => {
  assert.equal(exportWarning({ disk_fallbacks: 0 }), '');
  assert.equal(exportWarning(null), '');
  const text = exportWarning({ disk_fallbacks: 2 });
  assert.ok(text.includes('文件副本可能不含数据库直写记录'), text);
  assert.ok(text.includes('2'), text);

  // health 拉取失败（null）时仍必须保留 ticket 响应里的警告。
  const ticketText = exportWarning(null, {
    fileCopyIncomplete: true,
    diskFallbacks: 3,
  });
  assert.ok(ticketText.includes('文件副本可能不含数据库直写记录'), ticketText);
  assert.ok(ticketText.includes('3'), ticketText);
  assert.ok(exportWarning(null, { fileCopyIncomplete: true }).includes('不完整'));
  assert.equal(exportWarning(null, { fileCopyIncomplete: false, diskFallbacks: 0 }), '');
});

test('下载提示中性：只说明已发起，要求核对 complete/failed', () => {
  const text = exportStartedMessage('app-logs-x.ndjson');
  assert.ok(text.includes('已发起下载'), text);
  assert.ok(text.includes('app-logs-x.ndjson'), text);
  assert.ok(text.includes('complete/failed'), text);
  assert.ok(!text.includes('已成功'), text);
  assert.ok(!text.includes('已保存'), text);
});

test('导出票据校验：只接受同源下载 URL', () => {
  const ok = normalizeExportTicket({
    ticket: 'abc',
    url: '/api/v1/app-logs/export/download?ticket=abc',
    filename: 'app-logs-x.ndjson',
    expires_in: 60,
  });
  assert.equal(ok.ticket, 'abc');
  assert.equal(ok.filename, 'app-logs-x.ndjson');
  assert.equal(ok.expiresIn, 60);
  assert.equal(ok.fileCopyIncomplete, false);
  assert.equal(ok.diskFallbacks, 0);

  assert.throws(
    () =>
      normalizeExportTicket({
        ticket: 'abc',
        url: '/api/v1/app-logs/export/download?ticket=other',
      }),
    /不匹配/,
    'URL 中的 ticket 必须与 data.ticket 一致'
  );

  assert.throws(() => normalizeExportTicket({ ticket: 'abc' }), /票据/, '缺少 url 应拒绝');
  assert.throws(
    () => normalizeExportTicket({ ticket: 'abc', url: 'javascript:alert(1)' }),
    /导出/,
    '非导出 URL 应拒绝'
  );
  assert.throws(
    () => normalizeExportTicket({ ticket: '', url: '/api/v1/app-logs/export/download?ticket=x' }),
    /票据/,
    '空 ticket 应拒绝'
  );
  assert.throws(
    () =>
      normalizeExportTicket({
        ticket: 'abc',
        url: 'https://evil.example/api/v1/app-logs/export/download?ticket=abc',
      }),
    /导出/,
    '跨域 URL 应拒绝'
  );
});

test('原生 <a download> 触发下载，不把文件读入 JS 内存', async () => {
  const clicks = [];
  const link = {
    href: '',
    download: '',
    rel: '',
    style: {},
    click() {
      clicks.push(this.href);
    },
    remove() {},
  };
  const documentRef = {
    createElement(tag) {
      assert.equal(tag, 'a');
      return link;
    },
    body: {
      appendChild(node) {
        assert.equal(node, link);
      },
    },
  };
  const issued = [];
  const ticket = await startLogExport(
    { view: 'activity', keyword: '照片' },
    {
      issueTicket: async (params) => {
        issued.push(params);
        return {
          ticket: 'ticket-1',
          url: '/api/v1/app-logs/export/download?ticket=ticket-1',
          filename: 'app-logs-x.ndjson',
          expires_in: 60,
        };
      },
      onUnauthorized: () => {
        throw new Error('不应触发 401');
      },
      documentRef,
    }
  );
  assert.equal(issued.length, 1);
  assert.equal(issued[0].view, 'activity');
  assert.equal(ticket.filename, 'app-logs-x.ndjson');
  assert.equal(link.href, '/api/v1/app-logs/export/download?ticket=ticket-1');
  assert.equal(link.download, 'app-logs-x.ndjson');
  assert.deepEqual(clicks, ['/api/v1/app-logs/export/download?ticket=ticket-1']);
  assert.equal(link.style.display, 'none');
});

test('DOM 不可用时不得提示已发起下载', async () => {
  await assert.rejects(
    () =>
      startLogExport(
        {},
        {
          issueTicket: async () => ({
            ticket: 'ticket-x',
            url: '/api/v1/app-logs/export/download?ticket=ticket-x',
            filename: 'app-logs-x.ndjson',
            expires_in: 60,
          }),
        }
      ),
    /无法触发浏览器下载/
  );
});

test('导出票据 401 统一触发会话清理，已处理过的 401 不重复触发', async () => {
  const documentRef = { createElement: () => ({ click() {}, remove() {}, style: {} }) };
  let unauthorized = 0;
  const plain401 = Object.assign(new Error('登录已过期'), { status: 401, code: 'unauthorized' });
  await assert.rejects(
    () =>
      startLogExport(
        {},
        {
          issueTicket: async () => {
            throw plain401;
          },
          onUnauthorized: () => {
            unauthorized += 1;
          },
          documentRef,
        }
      ),
    /登录已过期/
  );
  assert.equal(unauthorized, 1);

  unauthorized = 0;
  const handled401 = Object.assign(new Error('登录已过期'), {
    status: 401,
    code: 'unauthorized',
    sessionHandled: true,
  });
  await assert.rejects(
    () =>
      startLogExport(
        {},
        {
          issueTicket: async () => {
            throw handled401;
          },
          onUnauthorized: () => {
            unauthorized += 1;
          },
          documentRef,
        }
      ),
    /登录已过期/
  );
  assert.equal(unauthorized, 0);
});
