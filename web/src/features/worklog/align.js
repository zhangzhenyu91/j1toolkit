// 派车对齐对照弹层的预填与模糊比对（与后端 dispatchalign.go / 旧版 worklog.html 同口径）：
// 车牌按本班启用字典精确命中、目的地去「中国/省/市/县/区」后互相包含、用车人按姓名点亮
// （表格人名不在成员字典时无法点亮，只能提示去「班组数据 → 人员」添加）。
// 差异字段按表格预填（车牌/目的地/派车单号），一致字段保留卡片现值。

// destSame 目的地模糊一致：表格常省略「市/县」等字样或带上级前缀
// （如卡片「孝义市」表格写「吕梁市孝义」）；表格目的地为空不约束。
export function destSame(cardDest, sheetTo) {
  const card = String(cardDest || '').trim();
  const sheet = String(sheetTo || '').trim();
  if (!sheet) return true;
  if (!card) return false;
  if (card === sheet || card.includes(sheet) || sheet.includes(card)) return true;
  const nc = card.replace(/中国|省|市|县|区/g, '');
  const ns = sheet.replace(/中国|省|市|县|区/g, '');
  if (nc.length < 2 || ns.length < 2) return false;
  return nc.includes(ns) || ns.includes(nc);
}

// findVehicle 表格车牌在本班启用字典的精确命中（不在字典返回 null）。
export function findVehicle(meta, plate) {
  if (!plate) return null;
  return (meta?.vehicles || []).find((v) => v.plate_no === plate) || null;
}

// findDestination 表格目的地在本班启用字典的模糊命中（空值/无命中返回 null）。
export function findDestination(meta, sheetTo) {
  if (!String(sheetTo || '').trim()) return null;
  return (meta?.destinations || []).find((d) => destSame(d.name, sheetTo)) || null;
}

// memberIdsByName 表格用车人按姓名点亮为本班成员 id（字典外姓名自然丢弃）。
export function memberIdsByName(meta, names) {
  const wanted = new Set(names || []);
  return (meta?.members || []).filter((m) => wanted.has(m.name)).map((m) => m.id);
}

// entryFormDefaults 当天其他卡片的编辑表单初值（无表格比对，保留卡片现值）。
export function entryFormDefaults(card) {
  return {
    vehicle_id: card?.vehicle_id || 0,
    destination_id: card?.destination_id || 0,
    dispatch_order_no: card?.order_no || '',
    member_ids: (card?.members || []).map((m) => m.member_id),
  };
}

// alignFormDefaults 对照弹层初始表单（matched 改卡 / sheetOnly 快速建卡）：
// 差异字段按表格预填，其余保留卡片现值；entryOnly 不用表单。
export function alignFormDefaults(item, meta) {
  const empty = { vehicle_id: 0, destination_id: 0, dispatch_order_no: '', member_ids: [] };
  if (!item) return empty;
  const sheet = item.sheet || {};
  const entry = item.entry || {};
  const diffs = item.diffs || {};

  if (item.kind === 'sheetOnly') {
    const plateHit = findVehicle(meta, sheet.plate);
    const destHit = findDestination(meta, sheet.to);
    return {
      vehicle_id: plateHit ? plateHit.id : 0,
      destination_id: destHit ? destHit.id : 0,
      dispatch_order_no: sheet.orderNo || '',
      member_ids: memberIdsByName(meta, sheet.members),
    };
  }

  const plateHit = diffs.plate ? findVehicle(meta, sheet.plate) : null;
  const destHit = diffs.dest ? findDestination(meta, sheet.to) : null;
  return {
    vehicle_id: plateHit ? plateHit.id : entry.vehicle_id || 0,
    destination_id: destHit ? destHit.id : entry.destination_id || 0,
    dispatch_order_no: diffs.orderNo ? sheet.orderNo || '' : entry.order_no || '',
    member_ids: (entry.members || []).map((m) => m.member_id),
  };
}
