// 题库搜题：Excel 题库解析（纯 JS，渲染层与 Node 测试共用，无 Electron / fs 依赖）。
// .xlsx 交给 read-excel-file（替代已停维护且有安全问题的 SheetJS xlsx），
// .xls（BIFF8 老格式）由 ./xls-reader.mjs 解析——read-excel-file 只支持 .xlsx，
// 但用户手上的题库不少仍是 .xls，导入能力必须保留。
// 产出口径与原实现一致：所有工作表按顺序拼成二维数组，第一张表保留表头
// （供 bank-store 识别列名），后续表若首行是「题干/答案」表头则丢掉重复表头。
//
// 模块形态：单份 ESM 实现（渲染层动态 import、Node 测试直接 import）。
// 别改回 .cjs——Vite dev 不转换 CJS，module.exports/require 会在浏览器里直接报错。
import readAllSheetsDefault from 'read-excel-file/universal';
import { readXls, isCfbBytes } from './xls-reader.mjs';

const XLSX_ERRORS = {
  XLS_FILE_NOT_SUPPORTED: '该文件是旧版 .xls 且无法解析，请用 Excel 另存为 .xlsx 后重试',
  FILE_NOT_SUPPORTED: '仅支持 .xls / .xlsx 文件',
  INVALID_ZIP: '文件已损坏（不是有效的 xlsx 压缩包）',
  NO_DATA: '文件为空',
  INPUT_TYPE_NOT_SUPPORTED: '文件内容无法识别',
};

function toUint8(input) {
  if (input instanceof Uint8Array) return input;
  if (input instanceof ArrayBuffer) return new Uint8Array(input);
  if (input && input.buffer instanceof ArrayBuffer) {
    return new Uint8Array(input.buffer, input.byteOffset || 0, input.byteLength);
  }
  throw new Error('文件内容为空或格式不支持');
}

export function isHeaderRow(row) {
  if (!Array.isArray(row)) return false;
  const cells = row.map((c) => String(c == null ? '' : c).trim());
  return cells.includes('题干') || cells.includes('答案');
}

// 多表拼接：首表保留表头，后续表若以表头开头则丢一行
export function mergeSheets(sheets) {
  const rows = [];
  sheets.forEach((s, i) => {
    const data = Array.isArray(s && s.data) ? s.data : [];
    if (data.length === 0) return;
    if (i > 0 && isHeaderRow(data[0])) rows.push(...data.slice(1));
    else rows.push(...data);
  });
  return rows;
}

async function readXlsxSheets(bytes, readAll) {
  const ab = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
  try {
    return await readAll(ab);
  } catch (e) {
    const code = e && e.code;
    if (code && XLSX_ERRORS[code]) {
      if (code === 'XLS_FILE_NOT_SUPPORTED') {
        // 扩展名是 .xlsx 但内容是老 .xls（另存时改错后缀）——按内容再试一次内置读取器
        if (isCfbBytes(bytes)) return readXls(bytes);
      }
      throw new Error(XLSX_ERRORS[code]);
    }
    throw new Error('Excel 解析失败：' + String((e && e.message) || e));
  }
}

/**
 * 读取题库文件为二维数组。
 * @param {ArrayBuffer|Uint8Array} input 文件字节
 * @param opts.readAllSheets 可选：替换用的 xlsx 读取函数（测试注入；缺省用 read-excel-file）
 * @returns {Promise<Array<Array<string|number|boolean>>>}
 */
export async function readWorkbookRows(input, opts) {
  const bytes = toUint8(input);
  if (bytes.length === 0) throw new Error('文件为空');
  const readAll =
    opts && typeof opts.readAllSheets === 'function' ? opts.readAllSheets : readAllSheetsDefault;
  const sheets = isCfbBytes(bytes) ? readXls(bytes) : await readXlsxSheets(bytes, readAll);
  const rows = mergeSheets(sheets);
  if (rows.length < 2) throw new Error('表格为空或缺少数据行');
  return rows;
}
