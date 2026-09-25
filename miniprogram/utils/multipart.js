// 手工拼装 multipart/form-data 的字节工具（pkg-safeday 生成提交、pkg-worklog 派车对齐共用）
// wx.uploadFile 单请求仅支持单文件，且 multipart 文件名只能是临时路径 basename
// （中文原名会丢失，sources 展示与扩展名校验都依赖原名），需一次提交多文件并保留原名时
// 按网页端 FormData 的字节格式自行拼装（UTF-8 编码文件名，服务端 multer 按 latin1 接收后
// 转回 UTF-8，与浏览器行为一致）

// 字符串 → UTF-8 字节 ArrayBuffer（含 surrogate pair 处理）
function utf8Buffer(str) {
  const bytes = [];
  for (let i = 0; i < str.length; i += 1) {
    let code = str.charCodeAt(i);
    if (code < 0x80) {
      bytes.push(code);
    } else if (code < 0x800) {
      bytes.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f));
    } else if (code >= 0xd800 && code <= 0xdbff) {
      const lo = str.charCodeAt(i + 1);
      i += 1;
      code = 0x10000 + (((code & 0x3ff) << 10) | (lo & 0x3ff));
      bytes.push(
        0xf0 | (code >> 18),
        0x80 | ((code >> 12) & 0x3f),
        0x80 | ((code >> 6) & 0x3f),
        0x80 | (code & 0x3f)
      );
    } else {
      bytes.push(0xe0 | (code >> 12), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f));
    }
  }
  return new Uint8Array(bytes).buffer;
}

function concatBuffers(buffers) {
  let total = 0;
  buffers.forEach((b) => { total += b.byteLength; });
  const out = new Uint8Array(total);
  let offset = 0;
  buffers.forEach((b) => {
    out.set(new Uint8Array(b), offset);
    offset += b.byteLength;
  });
  return out.buffer;
}

module.exports = { utf8Buffer, concatBuffers };
