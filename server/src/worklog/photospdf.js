// 水印照片 PDF 组合（pdf-lib，纯 JS 无原生依赖）：
// A4 横向；每张卡片的照片两两一页——两张 3:4 竖版并排（4:3 横版旋转 90° 竖放），
// 同一页只放同一卡片的照片；卡片余最后一张（或仅一张）时独占一页（居中），下一张卡片从新页开始
// 页边距与内网客户端「派车单·轨迹导出」一致（uvmp-toolkit/official_track.py：左 20mm 装订侧，其余 5mm）
const { PDFDocument, degrees } = require('pdf-lib');

const PAGE_W = 841.89; // A4 横向（pt）
const PAGE_H = 595.28;
const MM = 2.834645669;
const MARGIN_L = 20 * MM; // 左 20mm（装订侧，同轨迹导出）
const MARGIN_R = 5 * MM;
const MARGIN_TB = 5 * MM;
const GAP = 20;
const CONTENT_W = PAGE_W - MARGIN_L - MARGIN_R;
const CELL_W = (CONTENT_W - GAP) / 2;
const CELL_H = PAGE_H - MARGIN_TB * 2;

// 照片为 jpg（水印渲染产物），按 magic number 兜底 png
async function embedPhoto(pdfDoc, buf) {
  if (buf.length > 3 && buf[0] === 0x89 && buf[1] === 0x50) return pdfDoc.embedPng(buf);
  return pdfDoc.embedJpg(buf);
}

// 把照片放进左/右半页框（cellX 为框左缘），框内居中；横版（宽>高）旋转 90° 竖放
function drawPhoto(page, img, cellX) {
  const landscape = img.width > img.height;
  const dw = landscape ? img.height : img.width;
  const dh = landscape ? img.width : img.height;
  const scale = Math.min(CELL_W / dw, CELL_H / dh);
  const w = dw * scale;
  const h = dh * scale;
  const x = cellX + (CELL_W - w) / 2;
  const y = MARGIN_TB + (CELL_H - h) / 2;
  if (landscape) {
    // 逆时针 90°（照片底边→右边）；pdf-lib 旋转绕图左下角，需平移 x + w
    page.drawImage(img, { x: x + w, y, width: h, height: w, rotate: degrees(90) });
  } else {
    page.drawImage(img, { x, y, width: w, height: h });
  }
}

/**
 * 组合 PDF
 * @param {Array<{ photos: Buffer[] }>} cards 卡片顺序组（卡内照片按上传序）
 * @returns {Promise<Buffer>} PDF 文件内容
 */
async function buildPhotosPdf(cards) {
  const pdfDoc = await PDFDocument.create();
  for (const card of cards) {
    const photos = card.photos || [];
    for (let i = 0; i < photos.length; i += 2) {
      const pair = photos.slice(i, i + 2);
      const page = pdfDoc.addPage([PAGE_W, PAGE_H]);
      const imgs = [];
      for (const buf of pair) {
        // 逐张嵌入：坏图跳过不留空框（下载失败的张数由调用方统计提示）
        try { imgs.push(await embedPhoto(pdfDoc, buf)); } catch (err) { /* 跳过损坏照片 */ }
      }
      if (imgs.length === 1) {
        // 独占一页：同尺寸框在内容区内居中
        drawPhoto(page, imgs[0], MARGIN_L + (CONTENT_W - CELL_W) / 2);
      } else if (imgs.length === 2) {
        drawPhoto(page, imgs[0], MARGIN_L);
        drawPhoto(page, imgs[1], MARGIN_L + CELL_W + GAP);
      }
    }
  }
  return Buffer.from(await pdfDoc.save());
}

module.exports = { buildPhotosPdf };
