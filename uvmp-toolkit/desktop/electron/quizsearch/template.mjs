// 题库搜题：导入模板（渲染层生成，纯 JS，无 fs 依赖）。
// 用 write-excel-file 生成 .xlsx（替代 SheetJS xlsx 的写模板路径）：
// 列宽与外层单元格样式在这里定义，页面只负责拿到 blob → 交给主进程保存。
//
// 模块形态：单份 ESM 实现（渲染层动态 import、Node 测试直接 import）；
// .cjs 在 Vite dev 下会带 module.exports/require 进浏览器，直接报错（已踩坑）。
import writeXlsxFileDefault from 'write-excel-file/universal';

const COLUMNS = [
  { width: 40 },
  { width: 6 },
  { width: 22 },
  { width: 22 },
  { width: 22 },
  { width: 22 },
  { width: 12 },
  { width: 12 },
  { width: 8 },
  { width: 30 },
];

// 首行表头必须与 bank-store 的 HEADER_ALIASES 对齐
const HEADER = [
  '题干',
  '答案',
  '选项A',
  '选项B',
  '选项C',
  '选项D',
  '选项E',
  '选项F',
  '题型',
  '解析',
];

const ROWS = [
  HEADER,
  [
    '示例单选：我国安全生产工作的方针是？',
    'A',
    '安全第一、预防为主、综合治理',
    '质量第一',
    '效率优先',
    '效益优先',
    '',
    '',
    '',
    '2014 年安全生产法修订确立',
  ],
  [
    '示例多选：下列属于个人防护用品的有？',
    'ABD',
    '安全帽',
    '防护手套',
    '普通眼镜',
    '防护鞋',
    '',
    '',
    '多选',
    '',
  ],
  [
    '示例判断：安全生产，人人有责。',
    'A',
    '正确',
    '错误',
    '',
    '',
    '',
    '',
    '判断',
    '判断题 A=正确 B=错误；选项列可留空',
  ],
];

const TEMPLATE_FILE_NAME = '题库模板.xlsx';

// 生成模板文件（xlsx）；返回 Blob，调用方按需转 ArrayBuffer 交给主进程保存。
// writeXlsxFile 可注入（测试用），缺省用 read-excel-file 同族的 write-excel-file。
export async function buildTemplateBlob(opts) {
  const writeXlsxFile = (opts && opts.writeXlsxFile) || writeXlsxFileDefault;
  const data = ROWS.map((row, r) =>
    row.map((value) => ({
      value: String(value),
      fontWeight: r === 0 ? 'bold' : undefined,
      backgroundColor: r === 0 ? '#E8F0FF' : undefined,
    }))
  );
  const writer = writeXlsxFile(data, { sheet: '题库模板', columns: COLUMNS });
  return writer.toBlob();
}

export { TEMPLATE_FILE_NAME, HEADER, ROWS, COLUMNS };
