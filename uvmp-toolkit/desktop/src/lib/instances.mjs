// 应用实例（多开浏览器）的纯数据辅助：端口分配、默认实例与表单同步。
export function nextCdpPort(instances, base = 9222) {
  const used = new Set([9222]);
  for (const instance of instances || []) {
    const text = String(instance?.cdp_url || '');
    const match = text.match(/:(\d+)\/?$/);
    if (match) used.add(Number(match[1]));
  }
  let port = base;
  while (used.has(port)) port += 1;
  return `http://127.0.0.1:${port}`;
}

export function makeInstance(appId, instances, name, basePort = 9222) {
  const id = `${appId}-${Date.now()}`;
  return {
    id,
    name: name || `实例 ${(instances || []).length + 1}`,
    start_url: '',
    cdp_url: nextCdpPort(instances, basePort),
    profile_dir: '',
    params: {},
  };
}
