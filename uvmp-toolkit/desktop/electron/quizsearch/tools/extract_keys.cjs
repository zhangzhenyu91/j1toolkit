// 开发工具：从 RapidOCR 的 rec.onnx 模型 protobuf 元数据中提取内嵌字符字典（metadata_props key="character"），
// 落盘为 models/ppocr_keys.txt（每行一字符，UTF-8 LF）。onnxruntime-node 不暴露 modelMetadata，故直接扫二进制。
// 用法：node electron/quizsearch/tools/extract_keys.cjs
const fs = require('fs');
const path = require('path');

const modelsDir = path.join(__dirname, '..', 'models');
const recPath = path.join(modelsDir, 'rec.onnx');
const outPath = path.join(modelsDir, 'ppocr_keys.txt');

// 模式：0x0A 0x09 "character" 0x12 <varint 长度> <payload>（StringStringEntry 的 key/value 两个字段）
const marker = Buffer.concat([Buffer.from([0x0a, 0x09]), Buffer.from('character', 'utf8'), Buffer.from([0x12])]);
const buf = fs.readFileSync(recPath);
const at = buf.indexOf(marker);
if (at < 0) { console.error('未找到 character 元数据'); process.exit(1); }

let p = at + marker.length;
// varint 解码
let len = 0, shift = 0;
for (;;) {
  const b = buf[p++];
  len |= (b & 0x7f) << shift;
  if (!(b & 0x80)) break;
  shift += 7;
}
const payload = buf.slice(p, p + len).toString('utf8');
const chars = payload.split('\n');
if (chars.length < 1000) { console.error('字典过短，疑似解析错误：', chars.length); process.exit(1); }
fs.writeFileSync(outPath, chars.join('\n'), 'utf8');
console.log('提取字符数：', chars.length, '→', outPath);
console.log('前 10 字：', JSON.stringify(chars.slice(0, 10)));
