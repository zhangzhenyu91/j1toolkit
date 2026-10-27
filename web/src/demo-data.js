// 静态演示数据（DEMO ONLY）——仅供骨架页面演示，不请求真实服务、不代表真实业务数据。
// 后续接 Go API 时把 api.js 切到 live 适配层即可，页面组件不感知数据来源。
// 注意：本文件所有内容均为虚构占位，禁止把真实班组信息写进来。

// 工作台应用卡（对应后续 GET /api/v1/app/list）
export const demoApps = [
  {
    code: 'wechat-answer',
    name: '微信智能应答',
    description: '微信群消息按规则自动应答，支持会话捕获与结构化记录。',
    tag: '已接入',
    tagColor: 'green',
    route: '/wechat-answer',
  },
  {
    code: 'app-logs',
    name: '应用日志',
    description: '各应用运行日志查询，支持筛选与分页。',
    tag: '已接入',
    tagColor: 'green',
    route: '/app-logs',
  },
  {
    code: 'admin-config',
    name: '管理-应用配置',
    description: '运行时配置分组管理，敏感密钥不回显。',
    tag: '管理',
    tagColor: 'blue',
    route: '/admin/config',
  },
  {
    code: 'netdisk',
    name: '团队网盘',
    description: '班组文件共享与在线编辑（待接入）。',
    tag: '规划中',
    tagColor: 'orange',
    route: '',
  },
  {
    code: 'worklog',
    name: '出工日志',
    description: '出工记录与核验汇总（待接入）。',
    tag: '规划中',
    tagColor: 'orange',
    route: '',
  },
];

// 微信智能应答：群聊列表（演示状态）
export const demoChatGroups = [
  {
    code: 'g-demo-1',
    name: '检修一班交流群（演示）',
    members: 42,
    rulesEnabled: 3,
    rulesTotal: 4,
    lastActive: '2026-10-27 09:12',
  },
  {
    code: 'g-demo-2',
    name: '安全学习群（演示）',
    members: 56,
    rulesEnabled: 2,
    rulesTotal: 2,
    lastActive: '2026-10-26 17:40',
  },
  {
    code: 'g-demo-3',
    name: '树木排查协作群（演示）',
    members: 18,
    rulesEnabled: 1,
    rulesTotal: 3,
    lastActive: '2026-10-25 08:05',
  },
];

// 微信智能应答：规则（按群 code 分组，演示状态）
export const demoRules = {
  'g-demo-1': [
    {
      id: 'r-101',
      name: '出工签到提醒',
      keywords: '签到,出工',
      reply: '收到，请在小程序完成出工签到。',
      enabled: true,
    },
    {
      id: 'r-102',
      name: '值班电话查询',
      keywords: '值班电话',
      reply: '今日值班电话：见班组公告（演示占位）。',
      enabled: true,
    },
    {
      id: 'r-103',
      name: '天气提醒转发',
      keywords: '天气',
      reply: '已转发今日天气提示（演示占位）。',
      enabled: true,
    },
    {
      id: 'r-104',
      name: '旧版问候语',
      keywords: '早上好',
      reply: '早上好！（演示占位）',
      enabled: false,
    },
  ],
  'g-demo-2': [
    {
      id: 'r-201',
      name: '安规题库入口',
      keywords: '题库,安规',
      reply: '安规答题入口：见工作台应用卡（演示占位）。',
      enabled: true,
    },
    {
      id: 'r-202',
      name: '违章关键词捕获',
      keywords: '违章,未戴安全帽',
      reply: '已记录待核实（演示占位）。',
      enabled: true,
    },
  ],
  'g-demo-3': [
    {
      id: 'r-301',
      name: '树障会话捕获',
      keywords: '树障,树线矛盾',
      reply: '已捕获会话并生成树障记录草稿（演示占位）。',
      enabled: true,
    },
    {
      id: 'r-302',
      name: '区段登记',
      keywords: '区段',
      reply: '请补充区段与垂距信息（演示占位）。',
      enabled: false,
    },
    {
      id: 'r-303',
      name: '销号确认',
      keywords: '已处理,销号',
      reply: '已标记为处理完成（演示占位）。',
      enabled: false,
    },
  ],
};

// 应用日志（演示；messageCn 模拟「中文译文」，messageRaw 为「原文」，供切换演示）
export const demoLogs = [
  {
    id: 1001,
    app_code: 'wechat-answer',
    level: 'info',
    message: 'rule matched: r-101',
    message_cn: '规则命中：出工签到提醒',
    trace_id: 'tr-demo-001',
    created_at: '2026-10-27 09:12:03',
  },
  {
    id: 1002,
    app_code: 'wechat-answer',
    level: 'warn',
    message: 'dify workflow timeout after 30s',
    message_cn: 'Dify 工作流调用超时（30 秒）',
    trace_id: 'tr-demo-002',
    created_at: '2026-10-27 09:15:41',
  },
  {
    id: 1003,
    app_code: 'wechat-answer',
    level: 'error',
    message: 'ingest rejected: missing trace id',
    message_cn: '记录入库被拒绝：缺少链路 ID',
    trace_id: 'tr-demo-003',
    created_at: '2026-10-27 09:16:20',
  },
  {
    id: 1004,
    app_code: 'netdisk',
    level: 'info',
    message: 'share link created: s-demo-1',
    message_cn: '已创建公开分享链接',
    trace_id: 'tr-demo-004',
    created_at: '2026-10-26 20:02:11',
  },
  {
    id: 1005,
    app_code: 'netdisk',
    level: 'debug',
    message: 'cache refreshed: /docs',
    message_cn: '目录缓存已刷新：/docs',
    trace_id: 'tr-demo-005',
    created_at: '2026-10-26 20:02:12',
  },
  {
    id: 1006,
    app_code: 'worklog',
    level: 'info',
    message: 'daily summary generated',
    message_cn: '日汇总已生成',
    trace_id: 'tr-demo-006',
    created_at: '2026-10-26 18:30:00',
  },
  {
    id: 1007,
    app_code: 'worklog',
    level: 'warn',
    message: 'missing punch record for user 12',
    message_cn: '用户 12 缺打卡记录',
    trace_id: 'tr-demo-007',
    created_at: '2026-10-26 18:31:22',
  },
  {
    id: 1008,
    app_code: 'admin-config',
    level: 'info',
    message: 'config updated: notify.webhook',
    message_cn: '配置已更新：notify.webhook',
    trace_id: 'tr-demo-008',
    created_at: '2026-10-26 15:10:45',
  },
];

// 应用日志：应用子导航（演示）
export const demoLogApps = [
  { code: '', name: '全部应用' },
  { code: 'wechat-answer', name: '微信智能应答' },
  { code: 'netdisk', name: '团队网盘' },
  { code: 'worklog', name: '出工日志' },
  { code: 'admin-config', name: '管理-应用配置' },
];

// 管理-应用配置（演示；sensitive 项模拟「密钥不回显」：value 只存脱敏形状，真实值不在此出现）
export const demoConfigGroups = [
  {
    group: '基础配置',
    items: [
      {
        key: 'app.base_url',
        label: '服务地址',
        description: '对外访问的基础地址（普通配置，原样展示）',
        value: 'https://toolkit.example.invalid',
        sensitive: false,
      },
      {
        key: 'app.log_retain_days',
        label: '日志保留天数',
        description: '应用日志保留天数，超期自动清理',
        value: '7',
        sensitive: false,
      },
    ],
  },
  {
    group: '微信智能应答',
    items: [
      {
        key: 'crane.dify_token',
        label: 'Dify API 密钥',
        description: '调用 Dify Workflow 的密钥（敏感，不回显）',
        value: 'app****y7',
        sensitive: true,
      },
      {
        key: 'crane.ingest_token',
        label: '记录入库令牌',
        description: '树障记录直推入库的对接令牌（敏感，不回显）',
        value: 'sk****9f',
        sensitive: true,
      },
      {
        key: 'crane.capture_enabled',
        label: '会话捕获开关',
        description: '是否启用群会话捕获',
        value: 'true',
        sensitive: false,
      },
    ],
  },
  {
    group: '存储配置',
    items: [
      {
        key: 'storage.cos_secret_key',
        label: 'COS 密钥',
        description: '对象存储访问密钥（敏感，不回显）',
        value: 'AK****Qx',
        sensitive: true,
      },
      {
        key: 'storage.bucket',
        label: '存储桶',
        description: '对象存储桶名称',
        value: 'shade-demo-1250000000',
        sensitive: false,
      },
    ],
  },
];
