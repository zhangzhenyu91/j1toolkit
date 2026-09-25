/*
 * 工程记录水印 — 核心渲染模块（v2，按黑底参考图实测值校准）
 * 无 DOM 依赖：Node.js 端 require('./watermark.js') 使用。
 *
 * 用法：
 *   Watermark.draw(ctx, width, height, options)
 *     ctx    : CanvasRenderingContext2D（小程序 canvas type="2d" 的 ctx 同样适用）
 *     width  : 画布宽（px）
 *     height : 画布高（px）
 *     options: 见下方 defaults 对象，所有字段均可选
 *
 * 所有尺寸均为图片宽度 W 的比例（实测自 1200px 宽参考图），任意分辨率自适应。
 * 颜色与透明度实测方式：黑底参考图 C = F·α，与原照片（墙面底色已知）联立求解。
 */
(function () {
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
    codeFontFamily: '"PTMono", "HYQiHei", "Microsoft YaHei", sans-serif', // 防伪码码值字体（PT Mono Bold，等宽）
    codePrefixFontFamily: '"NotoSansSC", "HYQiHei", "Microsoft YaHei", sans-serif', // 「防伪」前缀字体（Noto Sans SC Bold）
    codeShadowImage: null,      // 码值底衬软影条（官方 9-patch 素材 DA.9.png，去 1px 标记边后拉伸）
    codeShadowLabelImage: null, // 「防伪」二字专用软影短条（code-shadow.png 裁中段首尾相接，见 SHADOW9 注释）
    fontWeight: ''            // 字重；汉仪旗黑 65J 本身即中黑，留空避免合成加粗
  };

  /* 实测几何/颜色参数（全部为图片短边 min(W,H) 的比例，基准图 1200x1600；
     缩放基准实测自 4160x3134 横版样图：横竖版水印绝对尺寸一致） */
  var M = {
    // 卡片（按 2160x2880 目标样图实测微调：左缘 0.02、宽 0.518、底边距 0.0213、头高 0.06）
    cardL: 0.02,     // 左边距
    cardW: 0.518,    // 卡片宽
    cardB: 0.0213,   // 底边距（卡片底到图片底；卡片向上生长，此值不变）
    radius: 0.016,   // 卡片圆角
    // 头部（纯色蓝，直接画在照片上——蓝块与白块不重叠、底下不再垫白块；
    // 按目标样图在其深色背景上的实测外观 rgb(79,121,196) 反解：rgb(84,147,245) @75%）
    headerH: 0.06,
    headerColor: 'rgba(69,136,249,0.7)',
    bodyColor: 'rgba(255,255,255,0.7)',
    // 黄点（颜色经用户确认）
    dotColor: '#FAC441',
    dotDia: 0.0146,
    dotCx: 0.028,    // 中心 x（相对卡片左缘）
    // 标题
    titleFont: 0.0355,
    titleCy: 0.0335, // 中心 y（相对卡片顶，头高 0.06 的 0.56 处，与原比例一致）
    titleDx: 0.018,  // 中心 x 相对卡片中心右移（视觉居中，避开左侧黄点）
    // 正文（内部 x 均为卡片宽度 cardW 的比例——实测：不同分辨率下冒号列恒为
    // 0.3233·cardW、值列恒为 ≈0.374·cardW，即布局随卡片整体缩放）
    labelFont: 0.033,
    labelX: 0.0302,   // 标签首字 x（相对 cardW）
    labelPitch: 0.076,// 标签字槽间距（相对 cardW）：标签逐字分散对齐，两字标签第 2 字落第 3 槽
    colonX: 0.3233,   // 冒号 x（相对 cardW）
    valueX: 0.3746,   // 值起始 x（相对 cardW）
    valuePadR: 0.033, // 值换行右边界到卡片右缘的距离（相对 cardW，与左侧留白对称）
    valueScaleX: 0.95,// 值文字横向缩放（原样图字形略窄于 HYQiHeiX2）
    row0Cy: 0.0258,   // 首行文字中心 y（相对头/身分界线，相对 B）
    rowPitch: 0.0475, // 行距（2160x2880 目标样图实测 ≈0.0476）
    bodyTopPad: 0.0092,   // 卡身首行上到分界线的距离（推算：cardH 分解）
    glyphH: 0.03,         // 字形高（用于卡片高度计算，随 labelFont 0.033 同步缩小）
    bodyBottomPad: 0.01,  // 末行下到卡片底的距离
    textColor: '#111111',
    // 右下品牌块（logo 按官方样图实测；防伪行右缘固定 codeRight 不动，
    // 整体等比缩小至「防」左缘刚好超过上方 logo 中「相」字左缘）
    logoW: 0.14,       // logo 图宽
    logoRight: 0.01,   // logo 右边距
    logoBottom: 0.025, // logo 底边距
    logoXiangL: 0.01,  // 「相」字左缘在 logo 图内的位置（相对图宽，按 alpha 通道实测 5/502）
    codeBeyondXiang: 0.001, // 「防」左缘超出「相」左缘的量（刚刚超过即可）
    codeFont: 0.0145,  // 防伪行基准字号：仅用于量宽，实际字号按「相」字对齐目标等比缩放
    codeFontScale: 1.1, // 码值字号相对前缀的放大倍数（Noto Sans SC CJK 可见高 ≈1.00em、PT Mono Bold 大写 0.70em，1.1× 使码值略小于「防伪」二字）
    codeXScale: 1,    // 码值横向压缩
    codeRight: 0.016,
    codeBaseline: 0.008, // 防伪码行基线（alphabetic）到底边的距离；行视觉中心≈0.0157
    codeColor: 'rgba(255,255,255,1)',
    codeTextShadowAlpha: 0.3, // 「防伪」与码值文字本体阴影不透明度（官方 setShadowLayer 同模式：纯黑、零偏移）
    codeTextShadowBlur: 3     // 文字本体阴影模糊半径（px，基准短边 1200，随 B 缩放）
  };

  /* 防伪行底衬软影条：官方 APK 素材 DA.9.png（Android NinePatch，155x26）。
   * 内容区 = 去 1px 标记边后的 153x24；右/下标记边给出内容留白：
   * 文字（可见字形盒）落于 padding box（列 7..142、行 6..17，即 135x12），
   * 软影条按 sx=行宽/135、sy=行高/12 各向拉伸后，文字左/上/右/下分别外扩 7/6/10/6 像素单位。
   * 黑色、平台 alpha≈66/255，上下余弦渐隐、两端短距渐隐。
   * 「防伪」二字字形小，按上述留白语义外扩量过大——专用短条 code-shadow-label.png
   * （内容区裁掉中间 73 列平台、左 40 列与右 40 列首尾相接而成，两端渐隐保留），
   * 绘制时字形盒仅外放 labelPad 倍字高小边距，边界仅微超出字边界。 */
  var SHADOW9 = { padL: 7, padT: 6, padR: 10, padB: 6, boxW: 135, boxH: 12, contentW: 153, contentH: 24,
    labelPad: 0.15 };

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
      return px + 'px ' + o.codeFontFamily;
    }
    function codePrefixFont(px) {
      return px + 'px ' + o.codePrefixFontFamily;
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
    // 天气为空时整行删除（卡片高度随总行数自适应减少）
    var fields = [
      ['施工内容', o.content],
      ['拍摄时间', o.time],
      ['天气', o.weather],
      ['地点', o.location],
      ['经度', o.longitude],
      ['纬度', o.latitude]
    ].filter(function (f) { return f[0] !== '天气' || String(f[1] || '').trim(); });
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

    // 卡身：白色半透明，仅头部分界线以下（下圆角；与头部蓝块互不重叠，不垫在蓝块下面）
    roundedPath(ctx, cardX, cardY + headerH, cardW, cardH - headerH, radius, [false, false, true, true]);
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

      // 防伪码：「防伪 」前缀与码值分字体绘制（前缀 NotoSansSC / 码值 PTMono）。
      // 横向：行右缘固定 codeRight 不动；先按基准字号量行宽，再等比缩小（纵横比不变）
      // 至「防」左缘刚好超过上方 logo 中「相」字左缘（logoXiangL / codeBeyondXiang）。
      // 中线对齐：两段可见字形的垂直中心在同一水平线（非底线对齐——Noto Sans SC CJK 比 PT Mono 大写高，
      // 底线对齐会让「防伪」中心偏高）；按 actualBoundingBox* 实测两段中心残差校正前缀基线，
      // 度量缺失时退回共用 alphabetic 基线（两字体中心本已近乎重合），任意分辨率恒平。
      // 阴影分两层（均按官方模式）：底层 = 软影条贴图垫在文字下（「防伪」与码值各自独立一条）；
      // 文字本体 = setShadowLayer 同模式纯黑零偏移投影（模糊半径随分辨率缩放）。
      ctx.fillStyle = M.codeColor;
      ctx.textBaseline = 'alphabetic';
      ctx.textAlign = 'left';
      var rightX = W - M.codeRight * B;
      var baselineY = H - M.codeBaseline * B;
      var codePrefix = '防伪 ';
      var codeFs = M.codeFont * B;
      // 基准字号下量行宽（前缀宽 + 码值宽），按目标行宽求等比缩放
      ctx.font = codePrefixFont(codeFs);
      var prefixW0 = ctx.measureText(codePrefix).width;
      ctx.font = codeFont(codeFs * M.codeFontScale);
      var codeW0 = ctx.measureText(o.antiCode).width * M.codeXScale;
      var xiangX = W - (M.logoRight + M.logoW - M.logoXiangL * M.logoW) * B; // 「相」字左缘
      codeFs *= (rightX - xiangX + M.codeBeyondXiang * B) / (prefixW0 + codeW0);
      // 浮点字号（不取整）：取整会让缩放后的行宽偏离目标 1-2px，左缘对不上「相」字
      ctx.font = codePrefixFont(codeFs);
      var preM = ctx.measureText('防');
      // 字形度量不可用的环境按实测常量兜底（Noto Sans SC CJK：上 0.88em / 下 0.12em）
      var preAsc = typeof preM.actualBoundingBoxAscent === 'number' ? preM.actualBoundingBoxAscent : codeFs * 0.88;
      var preDesc = typeof preM.actualBoundingBoxDescent === 'number' ? preM.actualBoundingBoxDescent : codeFs * 0.12;
      ctx.font = codeFont(codeFs * M.codeFontScale);
      var codeM = ctx.measureText(o.antiCode);
      var codeW = codeM.width * M.codeXScale;
      // PT Mono Bold 大写：上 0.70em；下探恒 0——码值字符集只有大写+数字，无下伸笔画，
      // 且 @napi-rs/canvas 的 actualBoundingBoxDescent 返回字体级下探（非字形实测），
      // 采信会让码值中心算偏高、「防伪」与码值中线不齐
      var codeAsc = typeof codeM.actualBoundingBoxAscent === 'number' ? codeM.actualBoundingBoxAscent : codeFs * M.codeFontScale * 0.70;
      var codeDesc = 0;
      ctx.font = codePrefixFont(codeFs);
      var prefixW = ctx.measureText(codePrefix).width;
      var prefixX = rightX - prefixW - codeW;
      // 前缀基线偏移 = 码值中心偏移 - 前缀中心偏移（各中心 = 基线 + (下探-上探)/2）
      var prefixBaselineY = baselineY + ((codeDesc - codeAsc) - (preDesc - preAsc)) / 2;

      // 底衬软影条：「防伪」与码值各自独立垫一条，互不相连。
      // 码值用整条 DA.9.png 内容区（153x24，去 1px 标记边），按 9-patch 内容留白语义
      // 拉伸——可见字形盒映射到 padding box（135x12），文字左/上/右/下外扩 7/6/10/6
      // 个缩放单位，渐隐与圆头均由素材自带，无需自绘。
      function codeShadow9(x, top, w, h) {
        var ssx = w / SHADOW9.boxW;
        var ssy = h / SHADOW9.boxH;
        ctx.drawImage(o.codeShadowImage, 1, 1, SHADOW9.contentW, SHADOW9.contentH,
          x - SHADOW9.padL * ssx, top - SHADOW9.padT * ssy,
          SHADOW9.contentW * ssx, SHADOW9.contentH * ssy);
      }
      if (o.codeShadowImage) {
        codeShadow9(rightX - codeW, baselineY - codeAsc, codeW, codeAsc + codeDesc);
      }
      // 「防伪」二字用裁短拼接的专用短条，字形盒仅外放一圈小边距（边界仅微超出字边界）
      if (o.codeShadowLabelImage) {
        var preGlyphW = ctx.measureText('防伪').width; // 前缀二字宽（不含尾部空格；此处 ctx.font 为前缀字体）
        var preH = preAsc + preDesc;
        var prePad = SHADOW9.labelPad * preH;
        ctx.drawImage(o.codeShadowLabelImage,
          prefixX - prePad, prefixBaselineY - preAsc - prePad, preGlyphW + prePad * 2, preH + prePad * 2);
      }

      // 文字本体阴影（官方 setShadowLayer 同模式）：纯黑、偏移 (0,0)、模糊半径随分辨率缩放
      ctx.shadowColor = 'rgba(0,0,0,' + M.codeTextShadowAlpha + ')';
      ctx.shadowBlur = M.codeTextShadowBlur * B / 1200;
      ctx.shadowOffsetX = 0;
      ctx.shadowOffsetY = 0;
      ctx.fillText(codePrefix, prefixX, prefixBaselineY);
      // 码值：字号放大 + 横向压缩（行宽不超限）
      ctx.save();
      ctx.translate(rightX - codeW, baselineY);
      ctx.scale(M.codeXScale, 1);
      ctx.font = codeFont(codeFs * M.codeFontScale);
      ctx.fillText(o.antiCode, 0, 0);
      ctx.restore();

      // 品牌 logo 图（抠自官方样图，含「今日水印/相机/真实可验」）
      // 阴影沿用原参数（颜色/模糊/偏移全量重设，不继承上方防伪行文字的阴影）
      if (o.brandImage) {
        var lw = M.logoW * B;
        var lh = lw * (o.brandImage.height / o.brandImage.width);
        ctx.shadowColor = 'rgba(0,0,0,0.35)';
        ctx.shadowBlur = 6 * B / 1200;
        ctx.shadowOffsetY = 2 * B / 1200;
        ctx.drawImage(o.brandImage, W - M.logoRight * B - lw, H - M.logoBottom * B - lh, lw, lh);
      }
      ctx.restore();
    }

    // 返回实际用到的防伪码/时间（自动生成时调用方需要取回）
    return { antiCode: o.antiCode, time: o.time };
  }

  var Watermark = {
    draw: draw,
    randomCode: randomCode
  };

  module.exports = Watermark;
})();
