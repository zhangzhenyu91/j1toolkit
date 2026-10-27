// 树障隐患演示数据（DEMO ONLY）：形状与 Go /api/v1/treehazard 响应 1:1，仅供 demo 模式渲染，
// 不发起网络请求、不代表真实班组数据。照片用内联 SVG dataURL 占位。
export function delay(data) {
  return new Promise((resolve) => setTimeout(() => resolve(structuredClone(data)), 120));
}

// 演示照片占位（SVG dataURL，仅演示渲染用）
const ph = (label) =>
  'data:image/svg+xml;utf8,' +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" width="320" height="240"><rect width="320" height="240" fill="#e8f0ff"/><text x="160" y="126" font-size="20" fill="#0e3da8" text-anchor="middle" font-family="sans-serif">${label}</text></svg>`
  );

export const teams = [
  { id: 1, name: '检修一班', status: 1 },
  { id: 2, name: '检修二班', status: 1 },
];

export const dashboard = {
  unhandled: 3,
  week_new: 2,
  over_threshold: 1,
  handled: 5,
  threshold: 8.5,
  pending_conflicts: 1,
  staging_batches: 1,
};

const photo = (id, recordId, kind, label) => ({
  id,
  record_id: recordId,
  kind,
  cos_key: `treehazard/demo/${id}.jpg`,
  url: ph(label),
  uploaded_by: 1,
  created_at: '2026-10-25T09:30:00+08:00',
});

const record = (o) => ({
  pole_from: null,
  pole_to: null,
  position_note: '',
  dist_small: '',
  dist_big: '',
  diameter: '',
  handler_name: '',
  handle_note: '',
  handled_at: '',
  source: 'crane',
  crane_session_id: null,
  crane_record_id: null,
  synced_at: null,
  created_by: null,
  created_at: '2026-10-24T10:00:00+08:00',
  updated_at: '2026-10-24T10:00:00+08:00',
  photos: [],
  ...o,
});

export const records = {
  threshold: 8.5,
  lines: [
    {
      voltage_level: '110',
      line_name: '金荆一线',
      records: [
        record({
          id: 101,
          team_id: 1,
          voltage_level: '110',
          line_name: '金荆一线',
          section: '3#-4#',
          tree_species: '杨树',
          tree_count: '2',
          horizontal: '4.5',
          vertical: '7.2',
          vertical_num: 7.2,
          finder_name: '张三',
          found_at: '2026-10-24',
          status: 'unhandled',
          photos: [photo(1, 101, 'evidence', '辅证照片')],
        }),
        record({
          id: 102,
          team_id: 1,
          voltage_level: '110',
          line_name: '金荆一线',
          section: '5#-6#',
          tree_species: '柳树',
          tree_count: '1',
          horizontal: '6',
          vertical: '12',
          vertical_num: 12,
          finder_name: '李四',
          found_at: '2026-10-25',
          status: 'handled',
          handler_name: '王五',
          handle_note: '削枝',
          handled_at: '2026-10-26',
          photos: [photo(2, 102, 'before', '处理前'), photo(3, 102, 'after', '处理后')],
        }),
      ],
    },
    {
      voltage_level: '220',
      line_name: '荆华二线',
      records: [
        record({
          id: 103,
          team_id: 1,
          voltage_level: '220',
          line_name: '荆华二线',
          section: '12#-13#',
          position_note: '线路左侧',
          diameter: '28',
          dist_small: '15',
          dist_big: '20',
          tree_species: '松树',
          tree_count: '3',
          horizontal: '5',
          vertical: '9',
          vertical_num: 9,
          finder_name: '赵六',
          found_at: '2026-10-26',
          status: 'unhandled',
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
      id: 104,
      team_id: 1,
      voltage_level: '35',
      line_name: '临时线路',
      section: '1#-2#',
      tree_species: '杨树',
      tree_count: '1',
      horizontal: '3',
      vertical: '6.8',
      vertical_num: 6.8,
      finder_name: '张三',
      found_at: '2026-10-26',
      status: 'unhandled',
    }),
  ],
};

export const stagingBatches = [
  {
    id: 11,
    team_id: 1,
    crane_session_id: 'sess-1',
    group_name: '树木排查群',
    pusher_name: 'crane-bot',
    review_note: '第 2 行区段格式待确认',
    xlsx_path: 'data/treehazard/inbox/1/sess-1.xlsx',
    status: 'pending',
    record_count: 2,
    created_at: '2026-10-26T20:00:00+08:00',
  },
];

const stagingRow = (o) => ({
  crane_record_id: '',
  dist_small: '',
  dist_big: '',
  diameter: '',
  horizontal: '',
  vertical: '',
  vertical_num: null,
  finder_name: '',
  found_at: '',
  handler_name: '',
  handle_note: '',
  handled_at: '',
  created_at: '2026-10-26T20:00:00+08:00',
  ...o,
});

export function stagingDetail(id) {
  const batch = stagingBatches.find((b) => b.id === Number(id)) || stagingBatches[0];
  return {
    batch,
    records: [
      stagingRow({
        id: 21,
        batch_id: batch.id,
        crane_record_id: 'rec-1',
        voltage_level: '110',
        line_name: '金荆一线',
        section: '8#-9#',
        tree_species: '杨树',
        tree_count: '2',
        horizontal: '4',
        vertical: '7',
        finder_name: '张三',
        found_at: '2026-10-26',
      }),
      stagingRow({
        id: 22,
        batch_id: batch.id,
        crane_record_id: 'rec-2',
        voltage_level: '110',
        line_name: '金荆一线',
        section: '9#-10#',
        tree_species: '柳树',
        tree_count: '1',
        horizontal: '5',
        vertical: '9.5',
        finder_name: '李四',
        found_at: '2026-10-26',
      }),
    ],
  };
}

// 冲突演示数据（内网同步产生的人工裁决样本；形状与 GET /conflicts 1:1）。
export const conflicts = [
  {
    id: 31,
    team_id: 1,
    record_id: 103,
    server_snapshot: {
      voltage_level: '220',
      line_name: '荆华二线',
      section: '12#-13#',
      tree_species: '松树',
      tree_count: '3',
      horizontal: '5',
      vertical: '9',
      finder_name: '赵六',
      found_at: '2026-10-26',
      handler_name: '',
      handle_note: '',
      handled_at: '',
    },
    intranet_snapshot: {
      voltage_level: '220',
      line_name: '荆华二线',
      section: '12#-13#',
      tree_species: '松树',
      tree_count: '4',
      horizontal: '5',
      vertical: '8.2',
      finder_name: '赵六',
      found_at: '2026-10-26',
      handler_name: '王五',
      handle_note: '已削枝',
      handled_at: '2026-10-27',
    },
    status: 'pending',
    created_at: '2026-10-27T02:10:00+08:00',
    voltage_level: '220',
    line_name: '荆华二线',
    section: '12#-13#',
    tree_species: '松树',
    tree_count: '3',
    dist_small: '15',
    dist_big: '20',
    diameter: '28',
    horizontal: '5',
    vertical: '9',
    vertical_num: 9,
    finder_name: '赵六',
    found_at: '2026-10-26',
    handler_name: '',
    handle_note: '',
    handled_at: '',
    record_status: 'unhandled',
  },
];
