// 团队网盘演示数据（DEMO ONLY）——仅供演示适配层回填，不请求真实服务、不代表真实业务数据。
// 禁止把真实班组/文件信息写进本文件。

// 空间列表（对应 GET /api/v1/netdisk/spaces）
export const demoSpaces = {
  max_upload_mb: 2048,
  spaces: [
    { key: 'my', name: '我的空间', manageable: true },
    { key: 'public', name: '公共区', team: '检修一班（演示）', manageable: true },
  ],
};

// 目录条目（对应 POST /api/v1/netdisk/list 的 items；_rel 为相对空间根路径，演示内部拼接）
export const demoDirs = {
  'my:': [
    { name: '安全日记录', is_dir: true, size: 0, modified: '2026-10-20 09:30:00' },
    { name: '一次设备台账.xlsx', is_dir: false, size: 231424, modified: '2026-10-25 15:12:00' },
    { name: '秋检总结.docx', is_dir: false, size: 86016, modified: '2026-10-22 10:05:00' },
    { name: '班组制度汇编.pdf', is_dir: false, size: 2519040, modified: '2026-09-30 16:40:00' },
  ],
  'my:安全日记录': [
    {
      name: '安全日活动记录-2026.10.20.docx',
      is_dir: false,
      size: 42393,
      modified: '2026-10-20 09:32:00',
    },
    {
      name: '安全日活动记录-2026.10.13.docx',
      is_dir: false,
      size: 41267,
      modified: '2026-10-13 09:28:00',
    },
  ],
  'public:': [
    { name: '标准化作业指导书', is_dir: true, size: 0, modified: '2026-10-11 11:00:00' },
    { name: '工器具清单.xlsx', is_dir: false, size: 118784, modified: '2026-10-18 14:22:00' },
  ],
  'public:标准化作业指导书': [
    {
      name: '开关检修作业指导书.docx',
      is_dir: false,
      size: 233472,
      modified: '2026-10-11 10:58:00',
    },
  ],
};

// 分享列表（对应 GET /api/v1/netdisk/shares 的行）
export const demoShares = [
  {
    share_id: 's-demo-0001',
    items: ['秋检总结.docx'],
    paths: ['/秋检总结.docx'],
    password: 'A1B2',
    creator: '演示用户',
    mine: true,
    expire_at: '2026-11-03 10:05:00',
    state: 'active',
    created_at: '2026-10-27 10:05:00',
  },
];
