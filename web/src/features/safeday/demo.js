// 安全日活动记录演示数据（DEMO ONLY）——仅供演示适配层回填，不请求真实服务、不代表真实业务数据。
// 禁止把真实班组/人员信息写进本文件。

import { BUILTIN_SECTIONS } from './templates.js';

// GET /api/v1/safeday/form-meta（成员名单与默认值，均为虚构）
export const demoFormMeta = {
  members: ['张三（演示）', '李四（演示）', '王五（演示）'],
  defaults: { superior: '', recorder: '李四（演示）', templateId: 'builtin' },
};

// GET /api/v1/safeday/templates 与 /api/v1/admin/safeday/templates 的演示模板：
// 均为虚构条目，hasApiKey 只是「已配置」状态标记，本文件不存放任何真实或占位密钥。
export const demoTemplates = [
  {
    id: 'builtin',
    name: '内置模板',
    mode: 'static',
    sections: BUILTIN_SECTIONS.map((s) => ({ key: s.key, title: s.title })),
    hasApiKey: false,
    isDefault: true,
    createdAt: '',
    updatedAt: '',
  },
  {
    id: 'tpl-demo-0001',
    name: '班组自定模板（演示）',
    mode: 'dynamic',
    sections: [
      { key: 'accident_process', title: '事故经过' },
      { key: 'cause_analysis', title: '原因分析' },
      { key: 'rectification_list', title: '整改清单' },
    ],
    sha256: '0'.repeat(64),
    size: 24576,
    hasApiKey: true,
    isDefault: false,
    createdAt: '2026-10-28 10:00:00',
    updatedAt: '2026-10-28 10:00:00',
  },
];

// 演示默认模板 id（与 demoTemplates 中 isDefault 一致）。
export const demoTemplateDefaultId = 'builtin';

// GET /api/v1/safeday/records 的 records 字段
export const demoRecords = [
  {
    id: 'r-demo-0001',
    name: '《安规学习》、《事故通报》',
    date: '2026.10.20',
    fileName: '安全日活动记录-2026.10.20.docx',
    team: '检修一班（演示）',
    status: 'done',
    sourceCount: 2,
    sources: ['安规学习.pdf', '事故通报.docx'],
    form: {
      host: '张三（演示）',
      superior: '',
      recorder: '李四（演示）',
      attendees: '',
      absentees: '',
      absentReason: '',
    },
    templateId: 'builtin',
    templateName: '内置模板',
    createdAt: '2026-10-20 09:32:00',
  },
  {
    id: 'r-demo-0002',
    name: '《秋检动员》',
    date: '2026.10.27',
    fileName: '安全日活动记录-2026.10.27.docx',
    team: '检修一班（演示）',
    status: 'processing',
    sourceCount: 1,
    sources: ['秋检动员.pptx'],
    form: {
      host: '张三（演示）',
      superior: '',
      recorder: '李四（演示）',
      attendees: '',
      absentees: '',
      absentReason: '',
    },
    templateId: 'tpl-demo-0001',
    templateName: '班组自定模板（演示）',
    createdAt: '2026-10-27 08:55:00',
  },
];
