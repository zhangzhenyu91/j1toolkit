/*
 * 工程记录水印 — 核心渲染模块（v2，按黑底参考图实测值校准）
 * 无 DOM 依赖：浏览器 <script> 引入后用 window.Watermark；
 * 微信小程序 / Node 用 require('./watermark.js')。
 *
 * 用法：
 *   Watermark.draw(ctx, width, height, options)
 *     ctx    : CanvasRenderingContext2D（小程序 canvas type="2d" 的 ctx 同样适用）
 *     width  : 画布宽（px）
 *     height : 画布高（px）
 *     options: 见 Watermark.defaults，所有字段均可选
 *
 * 所有尺寸均为图片宽度 W 的比例（实测自 1200px 宽参考图），任意分辨率自适应。
 * 颜色与透明度实测方式：黑底参考图 C = F·α，与原照片（墙面底色已知）联立求解。
 */
(function (global) {
  'use strict';

  var defaults = {
    title: '工程记录',        // 卡片标题
    content: '',              // 施工内容
    time: '',                 // 拍摄时间，空则取当前时间，格式 YYYY.MM.DD HH:mm
    weather: '',              // 天气，如 多云 30°C 南风2级
    location: '',             // 地点
    longitude: '',            // 经度，如 111.777658°E
    latitude: '',             // 纬度，如 37.271637°N
    antiCode: '',             // 防伪码，空则随机生成 14 位
    maxLines: 2,              // 每个值字段最多行数（超出换行截断加…）；施工内容/地点支持 \n 与自动换行
    showBrand: true,          // 右下角品牌块（logo 图 + 防伪码）开关
    brandImage: null,         // 品牌 logo 图片（HTMLImage 或小程序 canvas.createImage() 对象）
    fontFamily: '"HYQiHei", "Microsoft YaHei", sans-serif',
    codeFontFamily: '"PTMono", "HYQiHei", "Microsoft YaHei", sans-serif', // 防伪码码值字体（等宽）
    codePrefixFontFamily: '"SourceHanSansSC", "HYQiHei", "Microsoft YaHei", sans-serif', // 「防伪」前缀字体
    fontWeight: ''            // 字重；汉仪旗黑 65J 本身即中黑，留空避免合成加粗
  };

  /* 实测几何/颜色参数（全部为图片短边 min(W,H) 的比例，基准图 1200x1600；
     缩放基准实测自 4160x3134 横版样图：横竖版水印绝对尺寸一致） */
  var M = {
    // 卡片
    cardL: 0.0325,   // 左边距
    cardW: 0.5517,   // 卡片宽
    cardB: 0.0342,   // 底边距（卡片底到图片底；卡片向上生长，此值不变）
    radius: 0.016,   // 卡片圆角
    // 头部（纯色蓝，非渐变；颜色经用户确认 #156DFB，半透明 50%）
    headerH: 0.0708,
    headerColor: 'rgba(21,109,251,0.5)',    // #156DFB @50%
    bodyColor: 'rgba(255,255,255,0.72)',
    // 黄点（颜色经用户确认）
    dotColor: '#FAC441',
    dotDia: 0.0146,
    dotCx: 0.028,    // 中心 x（相对卡片左缘）
    // 标题
    titleFont: 0.0393,
    titleCy: 0.0396, // 中心 y（相对卡片顶）
    titleDx: 0.018,  // 中心 x 相对卡片中心右移（视觉居中，避开左侧黄点）
    // 正文（内部 x 均为卡片宽度 cardW 的比例——实测：不同分辨率下冒号列恒为
    // 0.3233·cardW、值列恒为 ≈0.374·cardW，即布局随卡片整体缩放）
    labelFont: 0.0363,
    labelX: 0.0302,   // 标签首字 x（相对 cardW）
    labelPitch: 0.076,// 标签字槽间距（相对 cardW）：标签逐字分散对齐，两字标签第 2 字落第 3 槽
    colonX: 0.3233,   // 冒号 x（相对 cardW）
    valueX: 0.3746,   // 值起始 x（相对 cardW）
    valuePadR: 0.033, // 值换行右边界到卡片右缘的距离（相对 cardW，与左侧留白对称）
    valueScaleX: 0.95,// 值文字横向缩放（原样图字形略窄于 HYQiHeiX2）
    row0Cy: 0.0258,   // 首行文字中心 y（相对头/身分界线，相对 B）
    rowPitch: 0.0493, // 行距（换行样图实测 0.0433~0.0493，取黑底样图实测值）
    bodyTopPad: 0.0092,   // 卡身首行上到分界线的距离（推算：cardH 分解）
    glyphH: 0.0333,       // 字形高（用于卡片高度计算）
    bodyBottomPad: 0.01,  // 末行下到卡片底的距离
    textColor: '#111111',
    // 右下品牌块（按官方样图实测：logo 视觉宽 0.132·B，防伪行左缘超出其左缘）
    logoW: 0.14,       // logo 图宽
    logoRight: 0.01,   // logo 右边距
    logoBottom: 0.025, // logo 底边距
    codeFont: 0.0145,
    codeRight: 0.016,
    codeCy: 0.0165,    // 防伪码文字中心到底边的距离
    codeColor: 'rgba(255,255,255,0.92)'
  };

  // 防伪码字符集：去掉 0/O、1/I 等易混淆字符
  var CODE_CHARS = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789';

  function randomCode(len) {
    len = len || 14;
    var out = '';
    for (var i = 0; i < len; i++) {
      out += CODE_CHARS.charAt(Math.floor(Math.random() * CODE_CHARS.length));
    }
    return out;
  }

  function pad2(n) { return n < 10 ? '0' + n : '' + n; }

  function formatTime(d) {
    return d.getFullYear() + '.' + pad2(d.getMonth() + 1) + '.' + pad2(d.getDate()) +
      ' ' + pad2(d.getHours()) + ':' + pad2(d.getMinutes());
  }

  /* 圆角路径（不用 ctx.roundRect，兼容小程序低版本基础库）
   * corners = [左上, 右上, 右下, 左下] 布尔数组 */
  function roundedPath(ctx, x, y, w, h, r, corners) {
    var tl = corners[0] ? r : 0;
    var tr = corners[1] ? r : 0;
    var br = corners[2] ? r : 0;
    var bl = corners[3] ? r : 0;
    ctx.beginPath();
    ctx.moveTo(x + tl, y);
    ctx.lineTo(x + w - tr, y);
    if (tr) ctx.arcTo(x + w, y, x + w, y + tr, tr);
    ctx.lineTo(x + w, y + h - br);
    if (br) ctx.arcTo(x + w, y + h, x + w - br, y + h, br);
    ctx.lineTo(x + bl, y + h);
    if (bl) ctx.arcTo(x, y + h, x, y + h - bl, bl);
    ctx.lineTo(x, y + tl);
    if (tl) ctx.arcTo(x, y, x + tl, y, tl);
    ctx.closePath();
  }

  function draw(ctx, W, H, opts) {
    var o = {};
    var k;
    for (k in defaults) o[k] = defaults[k];
    opts = opts || {};
    for (k in opts) o[k] = opts[k];

    if (!o.time) o.time = formatTime(new Date());
    if (!o.antiCode) o.antiCode = randomCode(14);

    function font(px) {
      return (o.fontWeight ? o.fontWeight + ' ' : '') + Math.round(px) + 'px ' + o.fontFamily;
    }
    function codeFont(px) {
      return Math.round(px) + 'px ' + o.codeFontFamily;
    }
    function codePrefixFont(px) {
      return Math.round(px) + 'px ' + o.codePrefixFontFamily;
    }

    /* 缩放基准：短边。横版照片水印不会随宽度放大（实测自 4160x3134 横版样图） */
    var B = Math.min(W, H);

    /* ================= 布局 ================= */
    var cardX = M.cardL * B;
    var cardW = M.cardW * B;
    var headerH = M.headerH * B;
    var radius = M.radius * B;
    var pitch = M.rowPitch * B;
    var labelX = cardX + M.labelX * cardW;
    var colonX = cardX + M.colonX * cardW;
    var valueX = cardX + M.valueX * cardW;
    var slotPitch = M.labelPitch * cardW;
    var fs = M.labelFont * B;
    var maxValueW = cardW * (1 - M.valuePadR) - M.valueX * cardW;

    /* ---- 1) 值换行：先按 \n 分段，段内再按宽度贪心换行 ---- */
    var fields = [
      ['施工内容', o.content],
      ['拍摄时间', o.time],
      ['天气', o.weather],
      ['地点', o.location],
      ['经度', o.longitude],
      ['纬度', o.latitude]
    ];
    ctx.font = font(fs);
    function wrapValue(text) {
      var segments = String(text).split('\n');
      var lines = [];
      for (var s = 0; s < segments.length; s++) {
        var seg = segments[s];
        var cur = '';
        for (var i = 0; i < seg.length; i++) {
          var ch = seg.charAt(i);
          if (cur && ctx.measureText(cur + ch).width * M.valueScaleX > maxValueW) {
            lines.push(cur);
            cur = ch;
          } else {
            cur += ch;
          }
        }
        lines.push(cur); // 空串也占位（显式空行）
      }
      if (lines.length > o.maxLines) { // 超出截断，末行加省略号
        lines = lines.slice(0, o.maxLines);
        var last = lines[lines.length - 1];
        while (last && ctx.measureText(last + '…').width * M.valueScaleX > maxValueW) {
          last = last.slice(0, -1);
        }
        lines[lines.length - 1] = last + '…';
      }
      return lines;
    }
    var wrapped = fields.map(function (f) {
      return { label: f[0], lines: wrapValue(f[1]) };
    });
    var totalLines = 0;
    wrapped.forEach(function (f) { totalLines += f.lines.length; });

    /* ---- 2) 卡片高度按行数动态计算，底边位置固定、向上生长 ---- */
    var topPad = M.bodyTopPad * B;
    var glyphH = M.glyphH * B;
    var bottomPad = M.bodyBottomPad * B;
    var cardH = headerH + topPad + pitch * (totalLines - 1) + glyphH + bottomPad;
    var cardY = H - M.cardB * B - cardH;
    if (cardY < M.cardL * B) cardY = M.cardL * B; // 极端长文本防顶出画面

    /* ================= 左下角卡片 ================= */
    ctx.save();

    // 卡身：白色半透明，四角圆
    roundedPath(ctx, cardX, cardY, cardW, cardH, radius, [true, true, true, true]);
    ctx.fillStyle = M.bodyColor;
    ctx.fill();

    // 头部：纯色半透明蓝，上圆角
    roundedPath(ctx, cardX, cardY, cardW, headerH, radius, [true, true, false, false]);
    ctx.fillStyle = M.headerColor;
    ctx.fill();

    // 黄色圆点
    ctx.beginPath();
    ctx.arc(cardX + M.dotCx * B, cardY + headerH / 2, M.dotDia * B / 2, 0, Math.PI * 2);
    ctx.fillStyle = M.dotColor;
    ctx.fill();

    // 标题
    ctx.fillStyle = '#FFFFFF';
    ctx.font = font(M.titleFont * B);
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillText(o.title, cardX + cardW / 2 + M.titleDx * B, cardY + M.titleCy * B);

    /* ================= 正文 =================
     * 标签逐字落槽分散对齐（4 字标签占 0-3 槽，2 字标签占 0、3 槽），
     * 冒号固定 colonX 列；值从 valueX 列起，续行只有值、与值列对齐 */
    var splitY = cardY + headerH;
    var cy = splitY + M.row0Cy * B;
    ctx.textAlign = 'left';
    ctx.fillStyle = M.textColor;
    ctx.font = font(fs);
    wrapped.forEach(function (f) {
      // 标签 + 冒号（仅该字段首行）
      for (var c = 0; c < f.label.length; c++) {
        var slot = f.label.length === 2 ? c * 3 : c;
        ctx.fillText(f.label.charAt(c), labelX + slot * slotPitch, cy);
      }
      ctx.fillText('：', colonX, cy);
      // 值各行（含续行）
      f.lines.forEach(function (line) {
        ctx.save();
        ctx.translate(valueX, cy);
        ctx.scale(M.valueScaleX, 1);
        ctx.fillText(line, 0, 0);
        ctx.restore();
        cy += pitch;
      });
    });
    ctx.restore();

    /* ================= 右下角品牌块 ================= */
    if (o.showBrand) {
      ctx.save();

      // 防伪码：「防伪 」前缀与码值分字体绘制（前缀 SourceHanSansSC / 码值 PTMono），整体右对齐
      ctx.shadowColor = 'rgba(0,0,0,0.35)';
      ctx.shadowBlur = 6 * B / 1200;
      ctx.shadowOffsetY = 2 * B / 1200;
      ctx.fillStyle = M.codeColor;
      ctx.textBaseline = 'middle';
      ctx.textAlign = 'left';
      var codeFs = M.codeFont * B;
      var codePrefix = '防伪 ';
      ctx.font = codeFont(codeFs);
      var codeW = ctx.measureText(o.antiCode).width;
      ctx.font = codePrefixFont(codeFs);
      var prefixW = ctx.measureText(codePrefix).width;
      var rightX = W - M.codeRight * B;
      var codeCy = H - M.codeCy * B;
      ctx.fillText(codePrefix, rightX - prefixW - codeW, codeCy);
      ctx.font = codeFont(codeFs);
      ctx.fillText(o.antiCode, rightX - codeW, codeCy);

      // 品牌 logo 图（抠自官方样图，含「今日水印/相机/真实可验」）
      if (o.brandImage) {
        var lw = M.logoW * B;
        var lh = lw * (o.brandImage.height / o.brandImage.width);
        ctx.shadowColor = 'rgba(0,0,0,0.35)';
        ctx.drawImage(o.brandImage, W - M.logoRight * B - lw, H - M.logoBottom * B - lh, lw, lh);
      }
      ctx.restore();
    }

    // 返回实际用到的防伪码/时间（自动生成时调用方需要取回）
    return { antiCode: o.antiCode, time: o.time };
  }

  var Watermark = {
    draw: draw,
    defaults: defaults,
    metrics: M,
    randomCode: randomCode,
    formatTime: formatTime
  };

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = Watermark;
  } else {
    global.Watermark = Watermark;
  }
})(typeof window !== 'undefined' ? window : this);
