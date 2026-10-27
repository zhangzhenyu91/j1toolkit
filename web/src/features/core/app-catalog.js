// 工作台/侧导航应用目录：sys_app.app_key → 网页端落地路由与展示标签。
// 与旧 Node 网页端 Shade.APP_NAV / APP_META 同口径（app_key 与 sys_app 种子一致）：
// 名称、描述、启用态以后端 GET /api/v1/app/list 为准，本文件只补接口不返回的路由与标签；
// 描述/标签仅在后端字段为空时兜底（同旧页「接口无此字段本地维护」的做法）。
// 无网页版的应用不给落地路由：wm-add（工作水印）为小程序能力，不硬造 Web 应用。
// icon 为 tdesign 同名线性图标（features/core/icons.js），后端 icon 为空时兜底。

// 管理类入口（不在工作台应用列表展示，仅超级管理员导航可见）
export const ADMIN_NAV_ITEMS = [
  { to: '/wechat-answer', label: '微信智能应答', icon: 'chat' },
  { to: '/app-logs', label: '应用日志', icon: 'view-list' },
  { to: '/admin/accounts', label: '管理-账号权限', icon: 'usergroup' },
  { to: '/admin/config', label: '管理-应用配置', icon: 'setting' },
];

// 应用目录：key 为 sys_app.app_key（含少量历史别名归一后的写法）。
// tags 与旧 index.html APP_META 同文（[文案, 标签样式]），样式取 tag-orange / tag-navy / tag-red。
const CATALOG = {
  'call-me': {
    route: '/callme',
    name: 'Call Me',
    icon: 'robot',
    tag: 'AI 问答',
    tagColor: 'orange',
    desc: '对接 WeKnora 企业知识库，规程、图纸与检修经验随问随答，流式输出、来源可溯。',
    tags: [
      ['AI 问答', 'tag-orange'],
      ['WeKnora', 'tag-navy'],
    ],
  },
  quiz: {
    route: '/quiz',
    name: '刷题练习',
    icon: 'task',
    tag: '刷题练习',
    tagColor: 'orange',
    desc: 'Excel 导入题库即导即练，单选 / 多选 / 判断全覆盖，错题自动归集专项攻克，AI 逐题生成解析。',
    tags: [
      ['刷题练习', 'tag-orange'],
      ['AI 解析', 'tag-navy'],
    ],
  },
  'work-log': {
    route: '/worklog',
    name: '出工日志',
    icon: 'calendar',
    tag: '记录存证',
    tagColor: 'orange',
    desc: '班组出工记录、水印照片存证与防伪码核验，月历视图一目了然，日志真实可查。',
    tags: [
      ['记录存证', 'tag-orange'],
      ['防伪核验', 'tag-navy'],
    ],
  },
  'wm-add': {
    route: '',
    name: '工作水印',
    icon: 'image',
    tag: '小程序',
    tagColor: 'blue',
    desc: '选片裁剪一键添加工作水印，地点天气与杆塔坐标随手带出，防伪码自动生成，仅回本人。',
    tags: [
      ['水印照片', 'tag-orange'],
      ['防伪码', 'tag-navy'],
      ['杆塔坐标', 'tag-navy'],
    ],
  },
  'safe-day': {
    route: '/safeday',
    name: '安全日',
    icon: 'file-safety',
    tag: '文件生成',
    tagColor: 'orange',
    desc: '上传班组活动 PDF / Office 文件，Dify 工作流自动生成活动记录，一键下载归档。',
    tags: [
      ['文件生成', 'tag-orange'],
      ['Dify', 'tag-navy'],
    ],
  },
  kvm: {
    route: '/kvm',
    name: 'KVM 远程',
    icon: 'terminal',
    tag: '远程终端',
    tagColor: 'orange',
    desc: '接入 GLKVM Cloud 设备中台，现场设备在线状态实时查看，一键开启远程终端与桌面控制。',
    tags: [
      ['远程终端', 'tag-orange'],
      ['桌面控制', 'tag-navy'],
    ],
  },
  'file-transfer': {
    route: '/kvm',
    name: '文件传输',
    icon: 'swap',
    tag: '文件传输',
    tagColor: 'blue',
    desc: '设备文件传输与 KVM 远程共用入口。',
    tags: [
      ['文件传输', 'tag-orange'],
      ['设备互联', 'tag-navy'],
    ],
  },
  netdisk: {
    route: '/netdisk',
    name: '团队网盘',
    icon: 'folder-open',
    tag: '网盘',
    tagColor: 'orange',
    desc: '个人空间与公共区文件存取，分片上传，外链分享。',
    tags: [
      ['网盘', 'tag-orange'],
      ['文件', 'tag-navy'],
      ['分享', 'tag-navy'],
    ],
  },
  'tree-hazard': {
    route: '/treehazard',
    name: '树障隐患',
    icon: 'forest',
    tag: '隐患台账',
    tagColor: 'orange',
    desc: '树障隐患按线路登记成账，处理进度与现场照片全程跟踪，统计报表一键导出。',
    tags: [
      ['隐患台账', 'tag-orange'],
      ['统计导出', 'tag-navy'],
    ],
  },
  crossing: {
    route: '/crossing',
    name: '交跨记录',
    icon: 'fork',
    tag: '交跨台账',
    tagColor: 'orange',
    desc: '输电线路交叉跨越台账：群内报送自动捕获入库，按线路归档，支持导出与内网同步。',
    tags: [
      ['交跨台账', 'tag-orange'],
      ['自动捕获', 'tag-navy'],
      ['统计导出', 'tag-navy'],
    ],
  },
  'team-data': {
    route: '/teamdata',
    name: '班组数据',
    icon: 'chart-bar',
    tag: '常用字典',
    tagColor: 'orange',
    desc: '车牌、目的地、人员与运维线路集中维护，供出工日志与树障记录统一取用。',
    tags: [
      ['常用字典', 'tag-orange'],
      ['线路维护', 'tag-navy'],
    ],
  },
  office: {
    route: '/office',
    name: '在线文档',
    icon: 'monitor',
    tag: 'ONLYOFFICE',
    tagColor: 'blue',
    desc: '网盘在线编辑与出工日志/安全日记录预览共用承载页。',
    tags: [
      ['ONLYOFFICE', 'tag-orange'],
      ['在线预览', 'tag-navy'],
    ],
  },
  // ---- 管理类（仅超级管理员可见；与 ADMIN_NAV_ITEMS 同路由）----
  'wechat-answer': {
    route: '/wechat-answer',
    name: '微信智能应答',
    icon: 'chat',
    tag: '管理',
    tagColor: 'blue',
    adminOnly: true,
    desc: '微信群消息按规则自动应答，支持会话捕获与结构化记录。',
    tags: [
      ['管理', 'tag-orange'],
      ['自动应答', 'tag-navy'],
    ],
  },
  'app-logs': {
    route: '/app-logs',
    name: '应用日志',
    icon: 'view-list',
    tag: '管理',
    tagColor: 'blue',
    adminOnly: true,
    desc: '各应用运行日志查询，支持筛选与分页。',
    tags: [
      ['管理', 'tag-orange'],
      ['运行日志', 'tag-navy'],
    ],
  },
  'admin-config': {
    route: '/admin/config',
    name: '管理-应用配置',
    icon: 'setting',
    tag: '管理',
    tagColor: 'blue',
    adminOnly: true,
    desc: '运行时配置分组管理，敏感密钥不回显。',
    tags: [
      ['管理', 'tag-orange'],
      ['运行配置', 'tag-navy'],
    ],
  },
};

// 历史别名 → 目录键归一（Go 模块名式 code 与 sys_app 种子键并存时都能落到同一条目）。
const ALIASES = {
  callme: 'call-me',
  worklog: 'work-log',
  wmadd: 'wm-add',
  safeday: 'safe-day',
  treehazard: 'tree-hazard',
  teamdata: 'team-data',
  filetransfer: 'file-transfer',
  'file-transfer': 'file-transfer',
  wechatanswer: 'wechat-answer',
  applogs: 'app-logs',
};

// canonicalKey 应用 code 归一（空值原样返回）。
export function canonicalKey(code) {
  const key = String(code || '').trim();
  if (!key) return '';
  return ALIASES[key] || key;
}

// metaForApp 取应用展示元数据；未登记的应用返回 null（调用方按后端字段直出）。
export function metaForApp(code) {
  const key = canonicalKey(code);
  return CATALOG[key] || null;
}

// routeForApp 应用落地路由；未登记或无 Web 端（wm-add）返回 ''。
export function routeForApp(code) {
  const meta = metaForApp(code);
  return (meta && meta.route) || '';
}

// iconForApp 应用图标名（tdesign 同名，见 features/core/icons.js）；未登记回退 app。
export function iconForApp(code, backendIcon) {
  if (backendIcon) return backendIcon;
  const meta = metaForApp(code);
  return (meta && meta.icon) || 'app';
}

// 侧导航应用条目（业务应用；管理类入口走 ADMIN_NAV_ITEMS）。
// 顺序与旧 Node 左侧导航（Shade.APP_NAV）一致。
export const NAV_ITEMS = [
  { to: '/worklog', label: '出工日志', icon: 'calendar' },
  { to: '/teamdata', label: '班组数据', icon: 'chart-bar' },
  { to: '/treehazard', label: '树障隐患', icon: 'forest' },
  { to: '/crossing', label: '交跨记录', icon: 'fork' },
  { to: '/netdisk', label: '团队网盘', icon: 'folder-open' },
  { to: '/safeday', label: '安全日', icon: 'file-safety' },
  { to: '/quiz', label: '刷题练习', icon: 'task' },
  { to: '/callme', label: 'Call Me', icon: 'robot' },
  { to: '/kvm', label: 'KVM 远程', icon: 'terminal' },
];
