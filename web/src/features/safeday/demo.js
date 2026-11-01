// 安全日活动记录演示数据（DEMO ONLY）——仅供演示适配层回填，不请求真实服务、不代表真实业务数据。
// 禁止把真实班组/人员信息写进本文件。

export const demoFormMeta = {
  members: ['张三（演示）', '李四（演示）', '王五（演示）'],
  defaults: { superior: '', recorder: '李四（演示）', templateId: 'tpl-demo-0001' },
};

export const demoTemplates = [
  {
    id: 'tpl-demo-0001',
    name: '班组自定模板（演示）',
    mode: 'dynamic',
    sections: [
      { key: 'accident_process', title: '一、事故经过' },
      { key: 'cause_analysis', title: '二、原因分析' },
      { key: 'rectification_list', title: '三、整改清单' },
    ],
    hasApiKey: true,
    isDefault: true,
    createdAt: '2026-10-28 10:00:00',
    updatedAt: '2026-10-28 10:00:00',
  },
];

export const demoTemplateDefaultId = 'tpl-demo-0001';

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
    templateId: 'tpl-demo-0001',
    templateName: '班组自定模板（演示）',
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
