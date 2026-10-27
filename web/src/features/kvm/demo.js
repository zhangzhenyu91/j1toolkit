// KVM 演示数据（DEMO ONLY）：形状与 Go /api/v1/kvm 响应 1:1，仅供 demo 模式渲染，
// 不发起任何网络请求、不代表真实设备。
export function delay(data) {
  return new Promise((resolve) => setTimeout(() => resolve(structuredClone(data)), 120));
}

export const teams = [
  { id: 1, name: '检修一班', status: 1 },
  { id: 2, name: '检修二班', status: 1 },
];

const DEVICES = [
  {
    id: 11,
    ddns: 'glkvm-demo-01',
    description: '值班室工控机',
    deviceGroupName: '检修一班',
    status: 'online',
    ip: '192.168.10.11',
    mac: 'AA:BB:CC:DD:EE:01',
    connectedTime: 1793040000,
    maintaining: false,
  },
  {
    id: 12,
    ddns: 'glkvm-demo-02',
    description: '检修间终端',
    deviceGroupName: '检修一班',
    status: 'offline',
    ip: '192.168.10.12',
    mac: 'AA:BB:CC:DD:EE:02',
    connectedTime: 1792950000,
    maintaining: false,
  },
  {
    id: 13,
    ddns: 'glkvm-demo-03',
    description: '仓库电脑',
    deviceGroupName: '检修一班',
    status: 'online',
    ip: '192.168.10.13',
    mac: 'AA:BB:CC:DD:EE:03',
    connectedTime: 1793040000,
    maintaining: true,
  },
];

export function devices(q) {
  const kw = (q || '').trim().toLowerCase();
  const items = DEVICES.filter(
    (d) =>
      !kw ||
      [d.description, d.ddns, d.ip, d.mac].some((s) =>
        String(s || '')
          .toLowerCase()
          .includes(kw)
      )
  );
  return {
    items: structuredClone(items),
    team: '检修一班',
    fallback: false,
    fallback_team: '',
  };
}

export const fileStatus = { shared: false, files: [] };

export const fileList = {
  files: [
    { name: '派车单.xlsx', size: 20480 },
    { name: '巡视记录.pdf', size: 102400 },
    { name: '现场照片.jpg', size: 2097152 },
  ],
};

export function netdiskList(path) {
  return {
    space: 'my',
    path: path || '/',
    manageable: true,
    items: [
      { name: '文件传输', is_dir: true, size: 0, modified: '2026-10-26 10:00:00' },
      { name: 'a.txt', is_dir: false, size: 128, modified: '2026-10-26 11:00:00' },
    ],
  };
}
