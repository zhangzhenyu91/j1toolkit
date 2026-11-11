// 题库搜题：旧版 .xls（BIFF8）读取器——纯 JS、无第三方依赖、浏览器/Node 通用。
// 为什么自己写：SheetJS（xlsx 0.18.5）已停止免费维护且有已知安全问题，题库导入改用
// read-excel-file（只支持 .xlsx）；但用户手上的题库有不少仍是 .xls，导入能力不能丢。
// 这里只实现题库导入需要的子集：OLE2 复合文档（CFB）→ Workbook 流 → BIFF8 的
// BOUNDSHEET / SST / LABELSST / LABEL / NUMBER / RK / MULRK / FORMULA / BOOLERR。
// 不支持：加密工作簿、BIFF5 及更早格式、条件格式/图表等非单元格内容（题库用不到）。
//
// 模块形态：单份 ESM 实现。渲染层用动态 import 加载，Vite dev 不做 CJS 转换——
// 写成 .cjs 会带着 module.exports/require 原样进浏览器直接报错（已踩坑），
// 因此题库解析链（含 workbook/template）一律 ESM，Node 测试同样 import 这份。

const CFB_MAGIC = [0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1];
const END_OF_CHAIN = 0xfffffffe;
const FREE_SECT = 0xffffffff;

export function isCfbBytes(bytes) {
  if (!bytes || bytes.length < 8) return false;
  for (let i = 0; i < 8; i++) if (bytes[i] !== CFB_MAGIC[i]) return false;
  return true;
}

function u16(b, o) {
  return b[o] | (b[o + 1] << 8);
}
function u32(b, o) {
  return (b[o] | (b[o + 1] << 8) | (b[o + 2] << 16) | (b[o + 3] << 24)) >>> 0;
}

// 读 CFB 中的一条流（按名字，忽略大小写）；返回 Uint8Array 或 null
function readCfbStream(bytes, wantedNames) {
  const sectorShift = u16(bytes, 30);
  const miniSectorShift = u16(bytes, 32);
  const sectorSize = 1 << sectorShift;
  const miniSectorSize = 1 << miniSectorShift;
  const miniCutoff = u32(bytes, 56);
  const firstDirSector = u32(bytes, 48);
  const firstMiniFat = u32(bytes, 60);
  const numDifat = u32(bytes, 72);
  const firstDifat = u32(bytes, 68);
  const sectorOffset = (n) => (n + 1) * sectorSize;

  // ---- DIFAT：头部 109 项 + 后续 DIFAT 扇区链
  const fatSectorIds = [];
  for (let i = 0; i < 109; i++) {
    const id = u32(bytes, 76 + i * 4);
    if (id !== FREE_SECT && id !== END_OF_CHAIN) fatSectorIds.push(id);
  }
  let difat = firstDifat;
  const seenDifat = new Set();
  for (let n = 0; n < numDifat && difat !== END_OF_CHAIN && difat !== FREE_SECT; n++) {
    if (seenDifat.has(difat)) break;
    seenDifat.add(difat);
    const base = sectorOffset(difat);
    if (base + sectorSize > bytes.length) break;
    const per = sectorSize / 4 - 1;
    for (let i = 0; i < per; i++) {
      const id = u32(bytes, base + i * 4);
      if (id !== FREE_SECT && id !== END_OF_CHAIN) fatSectorIds.push(id);
    }
    difat = u32(bytes, base + per * 4);
  }

  // ---- FAT
  const fat = [];
  for (const id of fatSectorIds) {
    const base = sectorOffset(id);
    if (base + sectorSize > bytes.length) continue;
    for (let i = 0; i < sectorSize / 4; i++) fat.push(u32(bytes, base + i * 4));
  }

  const readChain = (start) => {
    const out = [];
    const seen = new Set();
    let s = start;
    while (s !== END_OF_CHAIN && s !== FREE_SECT && s < fat.length) {
      if (seen.has(s)) break; // 环形链自保
      seen.add(s);
      const base = sectorOffset(s);
      if (base >= bytes.length) break;
      out.push(bytes.subarray(base, Math.min(base + sectorSize, bytes.length)));
      s = fat[s];
    }
    return out;
  };

  // ---- 目录
  const dirParts = readChain(firstDirSector);
  const entries = [];
  for (const part of dirParts) {
    for (let off = 0; off + 128 <= part.length; off += 128) {
      const nameLen = u16(part, off + 64);
      let name = '';
      for (let i = 0; i + 1 < nameLen && i < 64; i += 2) {
        const code = u16(part, off + i);
        if (code === 0) break;
        name += String.fromCharCode(code);
      }
      entries.push({
        name,
        type: part[off + 66],
        start: u32(part, off + 116),
        size: u32(part, off + 120),
      });
    }
  }

  const root = entries.find((e) => e.type === 5);
  const target = entries.find(
    (e) => e.type === 2 && wantedNames.some((n) => e.name.toLowerCase() === n.toLowerCase())
  );
  if (!target) return null;

  if (target.size >= miniCutoff || !root) {
    const parts = readChain(target.start);
    const out = new Uint8Array(target.size);
    let p = 0;
    for (const part of parts) {
      const n = Math.min(part.length, target.size - p);
      out.set(part.subarray(0, n), p);
      p += n;
      if (p >= target.size) break;
    }
    return out;
  }

  // ---- mini stream：小流以 64 字节迷你扇区存放在 root 的常规流里
  const miniFat = [];
  for (const part of readChain(firstMiniFat)) {
    for (let i = 0; i + 4 <= part.length; i += 4) miniFat.push(u32(part, i));
  }
  const miniParts = readChain(root.start);
  const miniStream = new Uint8Array(root.size);
  {
    let p = 0;
    for (const part of miniParts) {
      const n = Math.min(part.length, miniStream.length - p);
      miniStream.set(part.subarray(0, n), p);
      p += n;
      if (p >= miniStream.length) break;
    }
  }
  const out = new Uint8Array(target.size);
  let p = 0;
  let s = target.start;
  const seen = new Set();
  while (s !== END_OF_CHAIN && s !== FREE_SECT && s < miniFat.length && p < out.length) {
    if (seen.has(s)) break;
    seen.add(s);
    const base = s * miniSectorSize;
    const n = Math.min(miniSectorSize, out.length - p, Math.max(0, miniStream.length - base));
    if (n <= 0) break;
    out.set(miniStream.subarray(base, base + n), p);
    p += n;
    s = miniFat[s];
  }
  return out;
}

// 拼 SST 段并记录 CONTINUE 边界（跨段续读时首字节是新的字符宽度标志）
function sstStringReader(segments) {
  let total = 0;
  const boundaries = new Set();
  for (let i = 1; i < segments.length; i++) {
    total += segments[i - 1].length;
    boundaries.add(total);
  }
  const len = segments.reduce((n, s) => n + s.length, 0);
  const buf = new Uint8Array(len);
  let off = 0;
  for (const s of segments) {
    buf.set(s, off);
    off += s.length;
  }
  let p = 0;
  return {
    buf,
    get pos() {
      return p;
    },
    skip(n) {
      p += n;
    },
    u8() {
      return buf[p++];
    },
    u16() {
      const v = u16(buf, p);
      p += 2;
      return v;
    },
    u32() {
      const v = u32(buf, p);
      p += 4;
      return v;
    },
    chars(cch, high) {
      let width = high ? 2 : 1;
      let s = '';
      for (let i = 0; i < cch; i++) {
        if (boundaries.has(p)) {
          width = buf[p] === 1 ? 2 : 1;
          p++; // CONTINUE 首字节：本段字符宽度
        }
        if (p >= buf.length) break;
        if (width === 2) {
          s += String.fromCharCode(u16(buf, p));
          p += 2;
        } else {
          s += String.fromCharCode(buf[p]);
          p += 1;
        }
      }
      return s;
    },
  };
}

function decodeRk(rk) {
  let v;
  if (rk & 0x02) {
    v = rk >> 2; // 有符号 30 位整数
  } else {
    const b = new Uint8Array(8);
    const dv = new DataView(b.buffer);
    dv.setUint32(4, rk & 0xfffffffc, true);
    v = dv.getFloat64(0, true);
  }
  return rk & 0x01 ? v / 100 : v;
}

// 读 BOUNDSHEET 名称（BIFF8：cch 1 字节 + flags 1 字节）
function decodeSheetName(b, off, len) {
  const cch = b[off + 6];
  const flags = b[off + 7];
  let s = '';
  for (let i = 0; i < cch; i++) {
    if (flags & 0x01) s += String.fromCharCode(u16(b, off + 8 + i * 2));
    else s += String.fromCharCode(b[off + 8 + i]);
  }
  return s;
}

/**
 * 读取 .xls 的全部工作表。
 * @param {Uint8Array} bytes
 * @returns {Array<{sheet: string, data: Array<Array<string|number|boolean>>}>}
 */
export function readXls(bytes) {
  if (!isCfbBytes(bytes)) throw new Error('不是有效的 .xls 文件（OLE2 复合文档头不符）');
  const stream = readCfbStream(bytes, ['Workbook', 'Book']);
  if (!stream) throw new Error('未找到 Workbook 流，文件可能已损坏或被加密');
  // BOF 记录：id(2) + 长度(2) + vers(2) —— Excel 97 为 0x0600，BIFF5/更早为 0x0500 及以下
  const bofVersion = stream.length >= 6 ? u16(stream, 4) : 0;
  if (bofVersion !== 0x0600) {
    throw new Error('仅支持 Excel 97 及以上的 .xls；请在 Excel 中另存为 .xlsx 后重试');
  }

  const sheets = [];
  const byName = new Map();
  const sst = [];
  let current = null;
  let pendingFormula = null; // FORMULA 字符串结果在紧随的 STRING 记录里

  const sheetFor = (name) => {
    let s = byName.get(name);
    if (!s) {
      s = { sheet: name, cells: new Map(), maxRow: -1, maxCol: -1 };
      byName.set(name, s);
      sheets.push(s);
    }
    return s;
  };
  const put = (row, col, value) => {
    if (!current || row < 0 || col < 0) return;
    let line = current.cells.get(row);
    if (!line) {
      line = new Map();
      current.cells.set(row, line);
    }
    line.set(col, value);
    if (row > current.maxRow) current.maxRow = row;
    if (col > current.maxCol) current.maxCol = col;
  };

  let pos = 0;
  while (pos + 4 <= stream.length) {
    const id = u16(stream, pos);
    const size = u16(stream, pos + 2);
    const start = pos + 4;
    if (start + size > stream.length) break;
    const data = stream.subarray(start, start + size);

    if (id === 0x00fc) {
      // SST + 紧随的 CONTINUE 段
      const segments = [data];
      let next = start + size;
      while (next + 4 <= stream.length && u16(stream, next) === 0x003c) {
        const csize = u16(stream, next + 2);
        if (next + 4 + csize > stream.length) break;
        segments.push(stream.subarray(next + 4, next + 4 + csize));
        next += 4 + csize;
      }
      const r = sstStringReader(segments);
      r.u32(); // cstTotal
      const unique = r.u32();
      for (let i = 0; i < unique && r.pos < r.buf.length; i++) {
        const cch = r.u16();
        const flags = r.u8();
        const rich = (flags & 0x08) !== 0;
        const ext = (flags & 0x04) !== 0;
        const runs = rich ? r.u16() : 0;
        const extLen = ext ? r.u32() : 0;
        sst.push(r.chars(cch, flags & 0x01));
        if (rich) r.skip(runs * 4);
        if (ext) r.skip(extLen);
      }
      pos = next;
      continue;
    }

    if (id === 0x0085) {
      // BOUNDSHEET：位置 + 名称；流内顺序即工作表顺序
      const name = decodeSheetName(data, 0, size);
      const sheet = sheetFor(name);
      sheet.pos = u32(data, 0);
      pos = start + size;
      continue;
    }

    if (id === 0x0809) {
      // 每张表以 BOF 开头：把当前表切到该位置对应的表
      const found = sheets.find((s) => s.pos === pos);
      if (found) current = found;
      pos = start + size;
      continue;
    }

    if (id === 0x00fd && size >= 10) {
      // LABELSST
      put(u16(data, 0), u16(data, 2), sst[u32(data, 6)] || '');
    } else if (id === 0x0204 && size >= 8) {
      // LABEL（内联字符串）
      const cch = u16(data, 6);
      const flags = data[8];
      let s = '';
      for (let i = 0; i < cch; i++) {
        if (flags & 0x01) s += String.fromCharCode(u16(data, 9 + i * 2));
        else s += String.fromCharCode(data[9 + i]);
      }
      put(u16(data, 0), u16(data, 2), s);
    } else if (id === 0x0203 && size >= 14) {
      // NUMBER
      const dv = new DataView(data.buffer, data.byteOffset, data.byteLength);
      put(u16(data, 0), u16(data, 2), dv.getFloat64(6, true));
    } else if (id === 0x027e && size >= 10) {
      // RK
      put(u16(data, 0), u16(data, 2), decodeRk(u32(data, 6)));
    } else if (id === 0x00bd && size >= 6) {
      // MULRK：row + colFirst + (xf,rk)* + colLast
      const row = u16(data, 0);
      const colFirst = u16(data, 2);
      const count = (size - 6) / 6;
      for (let i = 0; i < count; i++) {
        put(row, colFirst + i, decodeRk(u32(data, 4 + i * 6 + 2)));
      }
    } else if (id === 0x0006 && size >= 14) {
      // FORMULA：缓存结果（row/col/ixfe 各 2 字节，结果自偏移 6 起）
      const row = u16(data, 0);
      const col = u16(data, 2);
      if (data[6] === 0xff && data[7] === 0xff) {
        // 非数值：data[8] 是类型（0=字符串、1=布尔、2=错误、3=空），字符串在紧随的 STRING 里
        pendingFormula = { row, col, kind: data[8] };
      } else {
        const dv = new DataView(data.buffer, data.byteOffset, data.byteLength);
        put(row, col, dv.getFloat64(6, true));
      }
    } else if (id === 0x0207 && pendingFormula) {
      // STRING：接上一条 FORMULA 的字符串结果
      if (pendingFormula.kind === 0) {
        const cch = u16(data, 0);
        const flags = data[2];
        let s = '';
        for (let i = 0; i < cch; i++) {
          if (flags & 0x01) s += String.fromCharCode(u16(data, 3 + i * 2));
          else s += String.fromCharCode(data[3 + i]);
        }
        put(pendingFormula.row, pendingFormula.col, s);
      }
      pendingFormula = null;
    } else if (id === 0x0205 && size >= 8) {
      // BOOLERR
      const row = u16(data, 0);
      const col = u16(data, 2);
      if (!data[7]) put(row, col, !!data[6]);
    }
    pos = start + size;
  }

  return sheets.map((s) => {
    const data = [];
    for (let r = 0; r <= s.maxRow; r++) {
      const line = new Array(s.maxCol + 1).fill('');
      const cells = s.cells.get(r);
      if (cells) for (const [c, v] of cells) line[c] = v;
      data.push(line);
    }
    return { sheet: s.sheet, data };
  });
}
