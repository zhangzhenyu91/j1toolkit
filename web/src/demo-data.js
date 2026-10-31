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
