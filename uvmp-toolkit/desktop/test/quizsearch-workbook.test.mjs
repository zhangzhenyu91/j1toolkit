// 题库导入解析与模板生成：xls / xlsx 两条路径 + 模板往返（纯 Node，不连任何外部服务）。
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { readWorkbookRows, mergeSheets, isHeaderRow } from '../electron/quizsearch/workbook.mjs';
import { buildTemplateBlob, TEMPLATE_FILE_NAME } from '../electron/quizsearch/template.mjs';
import { BankStore } from '../electron/quizsearch/bank-store.cjs';
import { readXls } from '../electron/quizsearch/xls-reader.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const XLS_FIXTURE = path.join(HERE, 'fixtures', 'quiz-bank.xls');

async function writeXlsx(sheets) {
  const { default: writeXlsxFile } = await import('write-excel-file/universal');
  const data = sheets.map((s) => ({
    sheet: s.sheet,
    data: s.data.map((row) => row.map((value) => ({ value: String(value) }))),
  }));
  const blob = await writeXlsxFile(data, {}).toBlob();
  return new Uint8Array(await blob.arrayBuffer());
}

// ---- 最小 BIFF8 造件：用于覆盖 Excel 真实文件里才会出现的记录组合 ----
function record(id, payload) {
  const out = new Uint8Array(4 + payload.length);
  const dv = new DataView(out.buffer);
  dv.setUint16(0, id, true);
  dv.setUint16(2, payload.length, true);
  out.set(payload, 4);
  return out;
}

function u16(...values) {
  const out = new Uint8Array(values.length * 2);
  const dv = new DataView(out.buffer);
  values.forEach((v, i) => dv.setUint16(i * 2, v, true));
  return out;
}

function bof(kind) {
  return record(0x0809, u16(0x0600, kind, 0x0dbb, 0x07cd, 0, 0, 0, 0));
}

function boundSheet(pos, name) {
  const payload = new Uint8Array(8 + name.length * 2);
  const dv = new DataView(payload.buffer);
  dv.setUint32(0, pos, true);
  payload[6] = name.length;
  payload[7] = 0x01; // 名称按 UTF-16LE 存
  for (let i = 0; i < name.length; i++) dv.setUint16(8 + i * 2, name.charCodeAt(i), true);
  return record(0x0085, payload);
}

// FORMULA：row/col/xf + 8 字节缓存结果（FF FF 带类型标记）+ grbit + chn
function formula(row, col, result) {
  const payload = new Uint8Array(20);
  const dv = new DataView(payload.buffer);
  dv.setUint16(0, row, true);
  dv.setUint16(2, col, true);
  payload.set(result, 6);
  return record(0x0006, payload);
}

function formulaString(row, col, kind) {
  return formula(row, col, [0xff, 0xff, kind, 0, 0, 0, 0, 0]);
}

function formulaNumber(row, col, value) {
  const result = new Uint8Array(8);
  new DataView(result.buffer).setFloat64(0, value, true);
  return formula(row, col, result);
}

function biffString(text) {
  const payload = new Uint8Array(3 + text.length * 2);
  const dv = new DataView(payload.buffer);
  dv.setUint16(0, text.length, true);
  payload[2] = 0x01; // UTF-16LE
  for (let i = 0; i < text.length; i++) dv.setUint16(3 + i * 2, text.charCodeAt(i), true);
  return record(0x0207, payload);
}

// 极简 OLE2/CFB 封装：FAT(1 扇区) + 目录(1 扇区) + Workbook 流（≥4096 字节走常规流）
function wrapCfb(stream) {
  const SECTOR = 512;
  const padded = new Uint8Array(Math.max(4096, Math.ceil(stream.length / SECTOR) * SECTOR));
  padded.set(stream);
  const streamSectors = padded.length / SECTOR;
  const buf = new Uint8Array(SECTOR * (2 + streamSectors + 1));
  const dv = new DataView(buf.buffer);
  buf.set([0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1], 0);
  dv.setUint16(24, 0x003e, true); // minor version
  dv.setUint16(26, 0x0003, true); // major version
  dv.setUint16(28, 0xfffe, true); // 小端字节序
  dv.setUint16(30, 9, true); // 512 字节扇区
  dv.setUint16(32, 6, true); // 64 字节迷你扇区
  dv.setUint32(40, 0, true); // v3 目录扇区数固定 0
  dv.setUint32(44, 1, true); // 1 个 FAT 扇区
  dv.setUint32(48, 1, true); // 目录在扇区 1
  dv.setUint32(56, 4096, true); // 迷你流阈值
  dv.setUint32(60, 0xfffffffe, true); // 无迷你 FAT
  dv.setUint32(64, 0, true);
  dv.setUint32(68, 0xfffffffe, true); // 无 DIFAT 扩展
  dv.setUint32(72, 0, true);
  for (let i = 1; i < 109; i++) dv.setUint32(76 + i * 4, 0xffffffff, true);
  dv.setUint32(76, 0, true); // DIFAT[0]：FAT 在扇区 0

  const FAT = SECTOR;
  for (let i = 0; i < SECTOR / 4; i++) dv.setUint32(FAT + i * 4, 0xffffffff, true);
  dv.setUint32(FAT, 0xfffffffd, true); // 扇区 0：FAT 自身
  dv.setUint32(FAT + 4, 0xfffffffe, true); // 扇区 1：目录链尾
  for (let i = 0; i < streamSectors; i++) {
    const s = 2 + i;
    dv.setUint32(FAT + s * 4, i === streamSectors - 1 ? 0xfffffffe : s + 1, true);
  }

  const dirEntry = (off, name, type, start, size) => {
    for (let i = 0; i < name.length; i++) dv.setUint16(off + i * 2, name.charCodeAt(i), true);
    dv.setUint16(off + 64, (name.length + 1) * 2, true); // 名称长度含结尾 0
    buf[off + 66] = type;
    dv.setUint32(off + 116, start, true);
    dv.setUint32(off + 120, size, true);
  };
  const DIR = SECTOR * 2;
  dirEntry(DIR, 'Root Entry', 5, 0xfffffffe, 0);
  dirEntry(DIR + 128, 'Workbook', 2, 2, padded.length);

  buf.set(padded, SECTOR * 3);
  return new Uint8Array(buf);
}

// 工作表 BOF 的绝对偏移由 BOUNDSHEET 指向；先把全局段拼好再算
function concatBytes(...parts) {
  const total = parts.reduce((n, p) => n + p.length, 0);
  const out = new Uint8Array(total);
  let off = 0;
  for (const p of parts) {
    out.set(p, off);
    off += p.length;
  }
  return out;
}

function formulaWorkbookStream() {
  const globals = bof(0x0005);
  const sheetName = '公式题';
  const eof = record(0x000a, new Uint8Array(0));
  const boundsLen = 4 + 8 + sheetName.length * 2;
  const sheetBofPos = globals.length + boundsLen + eof.length;
  const sheet = concatBytes(
    bof(0x0010),
    formulaString(0, 0, 0x00),
    biffString('公式题干：我国安全生产工作的方针是？'),
    formulaNumber(1, 0, 123.5),
    formulaString(2, 0, 0x01),
    biffString('布尔型公式不按字符串读'),
    formulaString(3, 0, 0x03),
    eof
  );
  return {
    sheetName,
    stream: concatBytes(globals, boundSheet(sheetBofPos, sheetName), eof, sheet),
  };
}

test('读取老 .xls：多工作表按序拼接，后续表丢掉重复表头', async () => {
  const bytes = new Uint8Array(fs.readFileSync(XLS_FIXTURE));
  const rows = await readWorkbookRows(bytes);
  assert.equal(rows[0][0], '题干');
  // 首表 3 条数据 + 次表 1 条数据，次表表头已去掉
  assert.equal(rows.length, 5);
  assert.equal(rows[1][0], '示例单选题干：我国安全生产工作的方针是？');
  assert.equal(rows[3][0], 123.5, '数值单元格保留原值');
  assert.equal(rows[4][0], '示例判断题干：安全生产，人人有责。');
});

test('读取 .xls 的公式单元格：缓存字符串题干由 FORMULA+STRING 还原', () => {
  const { stream, sheetName } = formulaWorkbookStream();
  const sheets = readXls(wrapCfb(stream));
  assert.equal(sheets.length, 1);
  assert.equal(sheets[0].sheet, sheetName);
  const data = sheets[0].data;
  assert.equal(data[0][0], '公式题干：我国安全生产工作的方针是？');
  assert.equal(data[1][0], 123.5, '数值型公式结果照旧');
  // 布尔/空值型公式（kind≠0）后紧跟的 STRING 不得当成字符串结果写回单元格
  assert.equal(data.length, 2, '非字符串型公式不得写入单元格');
  assert.deepEqual(
    data.flat().filter((v) => typeof v === 'string' && /布尔|不按字符串/.test(v)),
    []
  );
});

test('读取 .xlsx：read-excel-file 路径与 .xls 口径一致', async () => {
  const bytes = await writeXlsx([
    {
      sheet: '题库',
      data: [
        ['题干', '答案', '选项A', '选项B', '题型'],
        ['安全生产方针是什么？', 'A', '安全第一', '质量第一', '单选'],
        ['安全生产人人有责。', 'A', '正确', '错误', '判断'],
      ],
    },
    {
      sheet: '补充',
      data: [
        ['题干', '答案'],
        ['另一道补充题目内容', 'B'],
      ],
    },
  ]);
  const rows = await readWorkbookRows(bytes);
  assert.deepEqual(rows[0], ['题干', '答案', '选项A', '选项B', '题型']);
  assert.equal(rows.length, 4);
  assert.deepEqual(rows[3], ['另一道补充题目内容', 'B']);
});

test('模板下载内容可原样导回题库（写模板 → 读解析 → 导入）', async (t) => {
  const blob = await buildTemplateBlob();
  assert.equal(TEMPLATE_FILE_NAME, '题库模板.xlsx');
  assert.ok(blob.size > 0);
  const rows = await readWorkbookRows(new Uint8Array(await blob.arrayBuffer()));
  assert.deepEqual(rows[0].slice(0, 2), ['题干', '答案']);

  const tplDir = fs.mkdtempSync(path.join(os.tmpdir(), 'quiz-tpl-'));
  t.after(() => fs.rmSync(tplDir, { recursive: true, force: true }));
  const store = new BankStore(tplDir);
  const r = store.importRows('模板题库', TEMPLATE_FILE_NAME, rows);
  assert.equal(r.count, 3);
  assert.equal(r.dropped, 0);
  const bank = store.getBank(r.id);
  assert.deepEqual(
    bank.questions.map((q) => q.type),
    ['single', 'multiple', 'judge']
  );
  assert.equal(bank.questions[1].answer, 'ABD');
  assert.equal(bank.questions[2].answer, 'A');
});

test('空文件 / 缺数据行 / 损坏内容给出中文错误', async () => {
  await assert.rejects(() => readWorkbookRows(new Uint8Array()), /文件为空/);
  await assert.rejects(async () => {
    const bytes = await writeXlsx([{ sheet: 'S', data: [['题干', '答案']] }]);
    await readWorkbookRows(bytes);
  }, /缺少数据行/);
  await assert.rejects(
    () => readWorkbookRows(new Uint8Array([1, 2, 3, 4, 5, 6, 7, 8])),
    /解析失败|仅支持|损坏/
  );
});

test('扩展名错标（.xlsx 实为 .xls）仍按内容识别', async () => {
  const bytes = new Uint8Array(fs.readFileSync(XLS_FIXTURE));
  const sheets = readXls(bytes);
  assert.ok(sheets.length >= 2);
  const rows = await readWorkbookRows(
    bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength)
  );
  assert.equal(rows[0][0], '题干');
});

test('表头判定与多表拼接的边界情况', () => {
  assert.equal(isHeaderRow(['题干', '', '']), true);
  assert.equal(isHeaderRow(['答案']), true);
  assert.equal(isHeaderRow(['题干说明', '答案']), true);
  assert.equal(isHeaderRow(['题目']), false);
  assert.equal(isHeaderRow(null), false);

  const merged = mergeSheets([
    {
      sheet: 'a',
      data: [
        ['题干', '答案'],
        ['甲题', 'A'],
      ],
    },
    {
      sheet: 'b',
      data: [
        ['题干', '答案'],
        ['乙题', 'B'],
      ],
    },
    { sheet: 'c', data: [] },
    { sheet: 'd', data: [['丙题', 'C']] },
  ]);
  assert.deepEqual(merged, [
    ['题干', '答案'],
    ['甲题', 'A'],
    ['乙题', 'B'],
    ['丙题', 'C'],
  ]);
});

test('渲染层注入读取/写入实现时优先使用注入（不依赖模块内解析依赖）', async () => {
  const { default: writeXlsxFile } = await import('write-excel-file/universal');
  const { default: readAllSheets } = await import('read-excel-file/universal');

  let writeInjected = 0;
  const blob = await buildTemplateBlob({
    writeXlsxFile: (...args) => {
      writeInjected++;
      return writeXlsxFile(...args);
    },
  });
  assert.equal(writeInjected, 1);

  let readInjected = 0;
  const rows = await readWorkbookRows(new Uint8Array(await blob.arrayBuffer()), {
    readAllSheets: (...args) => {
      readInjected++;
      return readAllSheets(...args);
    },
  });
  assert.equal(readInjected, 1);
  assert.deepEqual(rows[0].slice(0, 2), ['题干', '答案']);
});
