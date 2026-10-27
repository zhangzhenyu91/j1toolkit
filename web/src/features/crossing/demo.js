// 交跨记录演示数据（DEMO ONLY）：形状与 Go /api/v1/crossing 响应 1:1，仅供 demo 模式渲染，
// 不发起网络请求、不代表真实班组数据。
export function delay(data) {
  return new Promise((resolve) => setTimeout(() => resolve(structuredClone(data)), 120));
}

export const teams = [
  { id: 1, name: '检修一班', status: 1 },
  { id: 2, name: '检修二班', status: 1 },
];

export const dashboard = {
  total: 4,
  week_new: 2,
  hazard: 1,
  pending_lines: 1,
  pending_conflicts: 1,
  staging_batches: 1,
};

const record = (o) => ({
  pole_from: null,
  pole_to: null,
  dist_small: '',
  dist_big: '',
  cross_horizontal: '',
  cross_angle: '',
  finder_name: '',
  inspect_at: null,
  remark: '',
  source: 'crane',
  crane_session_id: null,
  crane_record_id: null,
  synced_at: null,
  created_by: null,
  created_at: '2026-10-24T10:00:00+08:00',
  updated_at: '2026-10-24T10:00:00+08:00',
  ...o,
});

export const records = {
  lines: [
    {
      voltage_level: '110',
      line_name: '金荆一线',
      records: [
        record({
          id: 201,
          team_id: 1,
          voltage_level: '110',
          line_name: '金荆一线',
          section: '7#-8#',
          crossing_type: '10kV 配电线路',
          dist_small: '20',
          dist_big: '25',
          cross_horizontal: '8',
          cross_vertical: '11',
          cross_angle: '32',
          has_hazard: '否',
          finder_name: '张三',
          inspect_at: '2026-10-24',
        }),
        record({
          id: 202,
          team_id: 1,
          voltage_level: '110',
          line_name: '金荆一线',
          section: '15#-16#',
          crossing_type: '高速公路',
          dist_small: '40',
          dist_big: '35',
          cross_horizontal: '12',
          cross_vertical: '9.5',
          cross_angle: '18',
          has_hazard: '是',
          finder_name: '李四',
          inspect_at: '2026-10-25',
          remark: '垂距偏小，待复测',
        }),
      ],
    },
    {
      voltage_level: '220',
      line_name: '荆华二线',
      records: [
        record({
          id: 203,
          team_id: 1,
          voltage_level: '220',
          line_name: '荆华二线',
          section: '33#-34#',
          crossing_type: '铁路',
          dist_small: '50',
          dist_big: '55',
          cross_horizontal: '20',
          cross_vertical: '18',
          cross_angle: '75',
          has_hazard: '',
          finder_name: '王五',
          inspect_at: '2026-10-26',
        }),
      ],
    },
    {
      voltage_level: '35',
      line_name: '荆土线',
      records: [],
    },
  ],
  unmatched: [
    record({
      id: 204,
      team_id: 1,
      voltage_level: '35',
      line_name: '临时线路',
      section: '2#-3#',
      crossing_type: '乡道',
      cross_vertical: '12',
      cross_angle: '45',
      has_hazard: '',
      finder_name: '张三',
      inspect_at: '2026-10-26',
    }),
  ],
};

export const stagingBatches = [
  {
    id: 31,
    team_id: 1,
    crane_session_id: 'sess-9',
    group_name: '交跨排查群',
    pusher_name: 'crane-bot',
    review_note: '',
    xlsx_path: 'data/crossing/inbox/1/sess-9.xlsx',
    status: 'pending',
    record_count: 2,
    created_at: '2026-10-26T21:00:00+08:00',
  },
];

const stagingRow = (o) => ({
  crane_record_id: '',
  dist_small: '',
  dist_big: '',
  cross_horizontal: '',
  cross_vertical: '',
  cross_angle: '',
  has_hazard: '',
  finder_name: '',
  inspect_at: '',
  remark: '',
  created_at: '2026-10-26T21:00:00+08:00',
  ...o,
});

export function stagingDetail(id) {
  const batch = stagingBatches.find((b) => b.id === Number(id)) || stagingBatches[0];
  return {
    batch,
    records: [
      stagingRow({
        id: 41,
        batch_id: batch.id,
        crane_record_id: 'rec-1',
        voltage_level: '110',
        line_name: '金荆一线',
        section: '21#-22#',
        crossing_type: '10kV 配电线路',
        cross_vertical: '10',
        cross_angle: '28',
        finder_name: '张三',
        inspect_at: '2026-10-26',
      }),
      stagingRow({
        id: 42,
        batch_id: batch.id,
        crane_record_id: 'rec-2',
        voltage_level: '110',
        line_name: '金荆一线',
        section: '22#-23#',
        crossing_type: '通信光缆',
        cross_vertical: '14',
        cross_angle: '55',
        finder_name: '李四',
        inspect_at: '2026-10-26',
      }),
    ],
  };
}

// 冲突演示数据（内网同步产生的人工裁决样本；形状与 GET /conflicts 1:1）。
export const conflicts = [
  {
    id: 51,
    team_id: 1,
    record_id: 202,
    server_snapshot: {
      voltage_level: '110',
      line_name: '金荆一线',
      section: '15#-16#',
      crossing_type: '高速公路',
      dist_small: '40',
      dist_big: '35',
      cross_horizontal: '12',
      cross_vertical: '9.5',
      cross_angle: '18',
      has_hazard: '是',
      finder_name: '李四',
      inspect_at: '2026-10-25',
      remark: '垂距偏小，待复测',
    },
    intranet_snapshot: {
      voltage_level: '110',
      line_name: '金荆一线',
      section: '15#-16#',
      crossing_type: '高速公路',
      dist_small: '40',
      dist_big: '35',
      cross_horizontal: '12',
      cross_vertical: '8.6',
      cross_angle: '18',
      has_hazard: '是',
      finder_name: '李四',
      inspect_at: '2026-10-26',
      remark: '复测垂距 8.6m',
    },
    status: 'pending',
    created_at: '2026-10-27T02:20:00+08:00',
    voltage_level: '110',
    line_name: '金荆一线',
    section: '15#-16#',
    crossing_type: '高速公路',
    dist_small: '40',
    dist_big: '35',
    cross_horizontal: '12',
    cross_vertical: '9.5',
    cross_angle: '18',
    has_hazard: '是',
    finder_name: '李四',
    inspect_at: '2026-10-25',
    remark: '垂距偏小，待复测',
  },
];
