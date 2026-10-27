// 安全日活动记录演示数据（DEMO ONLY）——仅供演示适配层回填，不请求真实服务、不代表真实业务数据。
// 禁止把真实班组/人员信息写进本文件。

// GET /api/v1/safeday/form-meta（成员名单与默认值，均为虚构）
export const demoFormMeta = {
  members: ['张三（演示）', '李四（演示）', '王五（演示）'],
  defaults: { superior: '', recorder: '李四（演示）' },
};

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
    createdAt: '2026-10-27 08:55:00',
  },
];
