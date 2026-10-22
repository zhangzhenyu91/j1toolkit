// 照片 dataURL 解析（jpeg/png）：出工日志 / 水印添加 / 树障照片上传等共用的纯解析助手
// 仅做格式解析与 base64 解码，不做大小校验（各入口保留自己的错误码与限值口径）
// 解析成功 → { buf, contentType, ext }（contentType: image/jpeg|image/png，ext: jpg|png）；非 jpeg/png dataURL → null
function parseDataUrlPhoto(image) {
  const match = /^data:image\/(jpeg|jpg|png);base64,(.+)$/.exec(image || '');
  if (!match) return null;
  return {
    buf: Buffer.from(match[2], 'base64'),
    contentType: `image/${match[1] === 'png' ? 'png' : 'jpeg'}`,
    ext: match[1] === 'png' ? 'png' : 'jpg',
  };
}

module.exports = { parseDataUrlPhoto };
