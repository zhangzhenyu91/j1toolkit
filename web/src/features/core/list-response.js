// 列表响应归一助手：列表端点的 data 形状不一——有的直接返回数组，
// 有的包一层 {list|teams|items:[...]}，空数据时还可能是 null。
// 演示模式曾掩盖该差异（真实环境班组下拉为空），本助手统一归一为数组，供各 feature api.js 共用。

// normalizeList 把列表响应数据归一为数组：直接数组原样返回；
// 包装对象按 list → teams → items 顺序取第一个数组字段；
// 其余形状（null/undefined/标量/无数组字段）一律返回 []。
export function normalizeList(data) {
  if (Array.isArray(data)) return data;
  if (data && typeof data === 'object') {
    for (const key of ['list', 'teams', 'items']) {
      if (Array.isArray(data[key])) return data[key];
    }
  }
  return [];
}
