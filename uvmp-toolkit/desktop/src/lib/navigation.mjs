// 壳层导航与命令面板的纯数据/纯函数：侧栏、命令面板、设置搜索共用同一份元数据。
export const NAV_ITEMS = [
  {
    id: 'home',
    title: '首页',
    desc: '三项主应用与常用入口',
    icon: 'home',
    group: 'workbench',
  },
  {
    id: 'history',
    title: '执行历史',
    desc: '跨工具筛选记录、回看结果并回填参数',
    icon: 'history',
    group: 'workbench',
  },
  {
    id: 'pcd_export',
    title: '轨迹派车单导出',
    desc: '按派车单号批量导出派车单与轨迹 PDF',
    icon: 'route',
    group: 'primary',
    business: true,
  },
  {
    id: 'quiz_search',
    title: '题库搜题',
    desc: '屏幕选词实时匹配本机题库',
    icon: 'search',
    group: 'primary',
    business: true,
  },
  {
    id: 'course_autoplay',
    title: '学堂刷课',
    desc: '受控浏览器自动学习国网学堂课程',
    icon: 'course',
    group: 'primary',
    business: true,
  },
  {
    id: 'daily_sync',
    title: '派车同步',
    desc: '按计划把当日派车单导出到 GLKVM U 盘',
    icon: 'calendar',
    group: 'developer',
    business: true,
  },
  {
    id: 'tree_sync',
    title: '树障同步',
    desc: '按服务端 U 盘指令同步树障统计表',
    icon: 'forest',
    group: 'developer',
    business: true,
  },
  {
    id: 'crossing_sync',
    title: '交跨同步',
    desc: '按服务端 U 盘指令同步交跨统计表',
    icon: 'fork',
    group: 'developer',
    business: true,
  },
  {
    id: 'dev_script',
    title: '脚本测试',
    desc: '用客户端同款 Python 环境运行开发脚本',
    icon: 'flask',
    group: 'developer',
  },
  {
    id: 'settings',
    title: '设置',
    desc: '账号、宿主浏览器、导出与后台、快捷键与开发诊断',
    icon: 'setting',
    group: 'settings',
  },
];

const GROUPS = [
  { id: 'workbench', caption: '工作台', ids: ['home', 'history'] },
  { id: 'primary', caption: '主应用', ids: ['pcd_export', 'quiz_search', 'course_autoplay'] },
  {
    id: 'developer',
    caption: '开发者工具',
    ids: ['daily_sync', 'tree_sync', 'crossing_sync', 'dev_script'],
  },
];

export const BUSINESS_IDS = NAV_ITEMS.filter((item) => item.business).map((item) => item.id);

export function navItem(id) {
  return NAV_ITEMS.find((item) => item.id === id) || null;
}

export function navMatches(item, query) {
  const needle = String(query || '')
    .trim()
    .toLowerCase();
  if (!needle) return true;
  return (item.title + '\n' + item.desc).toLowerCase().includes(needle);
}

export function filterNavItems(query) {
  return NAV_ITEMS.filter((item) => item.group !== 'settings' && navMatches(item, query));
}

export function navGroups({ expandedDeveloper = false, query = '' } = {}) {
  const searching = String(query || '').trim() !== '';
  return GROUPS.filter((group) => group.id !== 'developer' || expandedDeveloper || searching)
    .map((group) => ({
      id: group.id,
      caption: group.caption,
      items: group.ids.map(navItem).filter((item) => item && navMatches(item, query)),
    }))
    .filter((group) => group.items.length > 0);
}

export function favoriteItems(favorites = []) {
  const selected = new Set(favorites);
  return BUSINESS_IDS.filter((id) => selected.has(id)).map(navItem);
}

export function toggleFavorite(favorites = [], id) {
  if (!BUSINESS_IDS.includes(id)) return [...favorites];
  const selected = new Set(favorites);
  if (selected.has(id)) selected.delete(id);
  else selected.add(id);
  return BUSINESS_IDS.filter((businessID) => selected.has(businessID));
}

export function commandPaletteItems({ favorites = [] } = {}) {
  const selected = new Set(favorites);
  const items = [];
  for (const item of NAV_ITEMS) {
    items.push({
      kind: 'navigate',
      title: item.title,
      desc: item.desc,
      pageID: item.id,
      icon: item.icon,
    });
    if (!item.business) continue;
    const favorite = selected.has(item.id);
    items.push({
      kind: 'toggleFavorite',
      title: (favorite ? '取消收藏 ' : '收藏 ') + item.title,
      desc: item.desc,
      pageID: item.id,
      icon: favorite ? 'star-filled' : 'star',
    });
  }
  return items;
}

export function filterCommandItems(items, query) {
  const needle = String(query || '')
    .trim()
    .toLowerCase();
  if (!needle) return items.slice();
  return items.filter((item) => (item.title + '\n' + item.desc).toLowerCase().includes(needle));
}

export const SETTINGS_SECTIONS = [
  {
    id: 'account',
    title: '账号 · 派车系统',
    fields: ['账号', '密码'],
    description:
      '派车系统的登录账号密码。只保存在本机配置文件，不用于其他用途；密码留空表示不修改。',
    keywords: ['SSO', '登录', '认证', '凭证', '用户名', '口令'],
  },
  {
    id: 'browser',
    title: '宿主浏览器',
    fields: ['浏览器', '当前生效'],
    description: '用于生成派车单和轨迹 PDF。通常保持自动检测即可；无法识别时，手动选择公司浏览器。',
    keywords: ['PDF', '渲染', '国密', '路径', '自动检测', 'Electron'],
  },
  {
    id: 'export-background',
    title: '导出与后台',
    fields: ['查询并发', '渲染并发', '关闭窗口时最小化到托盘', '开机自动启动', '执行方式'],
    description:
      '轨迹导出并行引擎的并发度，对下次导出任务生效。驻留执行更实时、状态直出客户端；机器注销/未登录时当天不执行，两种模式互斥。',
    keywords: ['导出', '并发', '后台', '托盘', '开机自启', '驻留', '系统定时器', '客户端', '调度'],
  },
  {
    id: 'shortcuts-about',
    title: '快捷键与关于',
    fields: ['工具搜索', '打开设置', '版本', '官方 Logo', '第三方许可'],
    description: 'Ctrl+K 搜索工具，Ctrl+, 打开设置；查看客户端版本、品牌与字体许可信息。',
    keywords: ['快捷键', '关于', '版本', 'Logo', 'Noto', 'Maple', 'OFL'],
  },
  {
    id: 'developer-diagnostics',
    title: '开发诊断',
    fields: [
      '开发模式',
      '自检日志',
      '检查 PDF 渲染',
      '检查派车系统登录',
      '检查浏览器渲染',
      '运行环境',
      '平台',
      '核心版本',
      '生效浏览器',
      '配置文件',
      '状态目录',
      '日志目录',
      '占用',
    ],
    description:
      '路径、自检、日志、开发开关；运行环境与路径排障；日志占用与清理；30 天前的任务日志在启动时自动清理。',
    keywords: ['诊断', '排障', '调试', '模拟数据', '日志', '清理', '自检', 'mock', 'CDP'],
  },
];

export function settingsMatches(query) {
  const needle = String(query || '')
    .trim()
    .toLowerCase();
  return SETTINGS_SECTIONS.map((section) => {
    if (!needle) return true;
    return [section.title, section.description, ...section.fields, ...section.keywords]
      .join('\n')
      .toLowerCase()
      .includes(needle);
  });
}

export function matchedSettingsIDs(query) {
  return SETTINGS_SECTIONS.filter((_, index) => settingsMatches(query)[index]).map(
    (section) => section.id
  );
}
