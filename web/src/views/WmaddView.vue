<template>
  <div class="wm-wrap">
    <!-- 应用权限门控（无 wm-add 权限时填充提示卡；初始为加载占位，同旧页 netdisk 口径） -->
    <div v-if="gate !== 'ok'" class="card gate-card">
      {{ gateText }}
    </div>

    <template v-else>
      <div class="wm-hero fade-up">
        <h2>工作水印</h2>
        <p>选片添加工作水印，服务端渲染仅回图——不上传、不存档、不验证</p>
      </div>

      <!-- 使用流程说明卡（文案同小程序口径，第 4 步为 Web 版落地面） -->
      <div class="card flow-card fade-up" style="animation-delay: 0.06s">
        <div class="flow-title">使用流程</div>
        <div class="flow-steps">
          <div class="fstep">
            <span class="no">1</span><span>拍摄 / 选择<span class="b">一张原图</span></span>
          </div>
          <div class="fstep">
            <span class="no">2</span
            ><span>非 4:3 / 3:4 时<span class="b">裁剪取景</span>（已是则免裁）</span>
          </div>
          <div class="fstep">
            <span class="no">3</span
            ><span><span class="b">编辑水印信息</span>，确认后由服务端渲染水印</span>
          </div>
          <div class="fstep">
            <span class="no">4</span
            ><span><span class="b">自动下载到本地</span>并全屏查看，仅本人持有</span>
          </div>
        </div>
      </div>

      <!-- 选片方式（与小程序起始页二选一同构） -->
      <div class="pick-grid fade-up" style="animation-delay: 0.12s">
        <button type="button" class="pick-opt" @click="onPick('camera')">
          <span class="pi">
            <!-- icons.js 无 camera，沿用旧页/工作台相机字形 -->
            <svg
              viewBox="0 0 24 24"
              width="22"
              height="22"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <path
                d="M4 8.5h2.6l1.7-2.6h7.4l1.7 2.6H20a1.5 1.5 0 0 1 1.5 1.5v9A1.5 1.5 0 0 1 20 20.5H4A1.5 1.5 0 0 1 2.5 19v-9A1.5 1.5 0 0 1 4 8.5z"
              />
              <circle cx="12" cy="13.8" r="3.6" />
            </svg>
          </span>
          <span class="ptext"
            ><span class="pt">拍摄照片</span
            ><span class="pn"
              >调起相机取原图，必裁 4:3 / 3:4（移动浏览器；桌面为文件选择）</span
            ></span
          >
        </button>
        <button type="button" class="pick-opt" @click="onPick('album')">
          <span class="pi"><LegacyIcon name="image" :size="22" /></span>
          <span class="ptext"
            ><span class="pt">选择图片</span
            ><span class="pn">已是 4:3 / 3:4 免裁剪直进编辑</span></span
          >
        </button>
      </div>

      <div class="wm-note fade-up" style="animation-delay: 0.18s">
        水印由服务端统一渲染（含防伪码与品牌标识），照片仅返回给本人，不进入出工记录、不参与验证；拍摄时间默认当前，定位带出经纬度（需浏览器授权），也可通过「选择杆塔坐标」带入。
      </div>
    </template>

    <!-- 选片 →（按需 4:3 裁剪）→ 字段编辑 → 服务端渲染 → 自动下载 + 结果预览 -->
    <CropDialog v-model:open="cropOpen" :bmp="cropBmp" :url="cropUrl" @done="onCropDone" />
    <WmFormDialog v-model:open="formOpen" :image="formImage" @rendered="onRendered" />
    <ResultDialog v-model:open="resultOpen" :image="resultImage" />
  </div>
</template>

<script setup>
// 工作水印（迁移 server/public/wmadd.html，app_key wm-add）：复制出工日志「拍摄/选择照片并添加水印」能力，
// 与出工日志解耦——不生成出工记录、无人名上下文，服务端渲染水印仅回图（不传 COS、不入库、不触发 Dify 验证）。
// 流程：拍摄/选择图片 →（拍摄必裁；已是 4:3/3:4 ±0.02 免裁）→ 4:3 裁剪层 → 编辑水印信息
//   （恒「无历史」口径：拍摄时间取当前、浏览器定位 WGS84→GCJ02 带出经纬度并调 /wmadd/geo 预填地点/天气、
//    「选择杆塔坐标」三级级联 ≤50m 波动带入、手动改经纬度 800ms 防抖刷新地点/天气、防伪码自动生成可换一换）
//   → POST /api/v1/wmadd/render 服务端渲染 → 自动下载到本地 + 结果弹层预览（点击全屏查看）。
// 与小程序的三处差异口径（同旧网页版）：
//   ① 网页端图片一律经 createImageBitmap + canvas 重绘导出（EXIF 方向自动归一，上送 orientation 恒 'up'）；
//   ② 浏览器 geolocation 为 WGS84，前端 wgs2gcj 换算为高德 GCJ02 口径再取值/逆解析（境外原样返回）；
//   ③ 「存相册 + wx.previewImage」对应为「自动下载 + 结果弹层（点击图片全屏预览）」，自动下载被浏览器拦截时可手动点「下载图片」。
import { onMounted, onUnmounted, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { listApps } from '../api.js';
import { canonicalKey } from '../features/core/app-catalog.js';
import LegacyIcon from '../features/core/LegacyIcon.vue';
import CropDialog from '../features/wmadd/CropDialog.vue';
import ResultDialog from '../features/wmadd/ResultDialog.vue';
import WmFormDialog from '../features/wmadd/WmFormDialog.vue';
import { bitmapToJpeg, pickImageFile } from '../features/wmadd/wm-utils.js';

// ---- 应用权限门控（/api/v1/app/list 查 wm-add，同旧页 netdisk 口径） ----
const gate = ref('loading'); // loading | ok | denied | error
const gateText = ref('加载中…');

onMounted(async () => {
  try {
    const list = await listApps();
    const granted = (list || []).some((a) => canonicalKey(a.code || a.app_key || '') === 'wm-add');
    if (!granted) {
      gate.value = 'denied';
      gateText.value = '暂无「工作水印」应用权限，请联系管理员开通。';
      return;
    }
    gate.value = 'ok';
  } catch (e) {
    gate.value = 'error';
    gateText.value = '加载失败，请刷新重试。';
  }
});

// ---- 选片 →（按需 4:3 裁剪）→ 字段编辑 ----
const cropOpen = ref(false);
const cropBmp = ref(null);
const cropUrl = ref('');
const formOpen = ref(false);
const formImage = ref('');
const resultOpen = ref(false);
const resultImage = ref('');

// releaseCrop 释放裁剪层资源（objectURL + 位图；确认/取消/关闭幂等释放）
function releaseCrop() {
  if (cropUrl.value) {
    URL.revokeObjectURL(cropUrl.value);
    cropUrl.value = '';
  }
  if (cropBmp.value && cropBmp.value.close) cropBmp.value.close();
  cropBmp.value = null;
}

// 裁剪层关闭（取消/确认后）即释放资源，页面卸载兜底释放
watch(cropOpen, (open) => {
  if (!open) releaseCrop();
});
onUnmounted(releaseCrop);

// onPick 唤起系统文件选择（capture=true 移动浏览器直接调起相机）；
// createImageBitmap 默认按 EXIF 方向解码（宽高即显示宽高），后续 canvas 重绘导出 JPEG 方向已归一。
// 分流同小程序：拍摄必裁；相册已为 4:3/3:4（±0.02 容差）免裁直进表单。
async function onPick(src) {
  const file = await pickImageFile(src === 'camera');
  if (!file) return;
  if (!/^image\//.test(file.type || '')) {
    MessagePlugin.info('请选择图片文件');
    return;
  }
  let bmp;
  try {
    bmp = await createImageBitmap(file);
  } catch (e) {
    MessagePlugin.error('图片读取失败');
    return;
  }
  const ratio = bmp.width / bmp.height;
  const is43 = Math.abs(ratio - 4 / 3) <= 0.02 || Math.abs(ratio - 3 / 4) <= 0.02;
  if (src === 'album' && is43) {
    formImage.value = bitmapToJpeg(bmp, 4096);
    if (bmp.close) bmp.close();
    formOpen.value = true;
    return;
  }
  cropBmp.value = bmp;
  cropUrl.value = URL.createObjectURL(file);
  cropOpen.value = true;
}

function onCropDone(image) {
  releaseCrop();
  formImage.value = image;
  formOpen.value = true;
}

function onRendered(out) {
  resultImage.value = out;
  resultOpen.value = true;
}
</script>

<style scoped>
/* ===== 版心（工具页自限宽阅读列）与页首（同旧页 .wm-wrap/.wm-hero） ===== */
.wm-wrap {
  max-width: 720px;
  margin: 0 auto;
}
.gate-card {
  margin-top: 22px;
  padding: 40px 26px;
  text-align: center;
  font-size: 13px;
  color: var(--tk-ph);
}
.wm-hero h2 {
  margin: 0;
  font-size: 21px;
  font-weight: 700;
  letter-spacing: 1px;
}
.wm-hero p {
  margin-top: 7px;
  font-size: 12.5px;
  color: var(--tk-ph);
  letter-spacing: 1px;
}

/* ===== 使用流程卡（同小程序流程说明卡文案口径） ===== */
.flow-card {
  padding: 20px 24px;
  margin-top: 18px;
}
.flow-title {
  font-size: 15px;
  font-weight: 600;
  letter-spacing: 1px;
}
.flow-steps {
  margin-top: 14px;
  display: grid;
  gap: 10px;
}
.fstep {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  font-size: 13px;
  color: var(--tk-sub);
  line-height: 1.75;
}
.fstep .no {
  flex: none;
  width: 20px;
  height: 20px;
  margin-top: 2px;
  border-radius: 50%;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  font-size: 11px;
  font-weight: 700;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-variant-numeric: tabular-nums;
}
.fstep .b {
  color: var(--tk-ink);
  font-weight: 600;
}

/* ===== 选片方式（与小程序起始页同构：浅蓝图标块 + 标题 + 说明，hover 上浮影加深） ===== */
.pick-grid {
  margin-top: 16px;
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 14px;
}
.pick-opt {
  display: flex;
  align-items: center;
  gap: 14px;
  text-align: left;
  font-family: inherit;
  background: var(--tk-card);
  border: none;
  border-radius: 12px;
  box-shadow: var(--tk-shadow-card);
  padding: 18px;
  cursor: pointer;
}
.pick-opt:hover {
  transform: translateY(-2px);
  box-shadow: var(--tk-shadow-hover);
}
.pick-opt .pi {
  flex: none;
  width: 44px;
  height: 44px;
  border-radius: 8px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  display: flex;
  align-items: center;
  justify-content: center;
}
.pick-opt .ptext {
  display: block;
  min-width: 0;
}
.pick-opt .pt {
  display: block;
  font-size: 14px;
  font-weight: 600;
  letter-spacing: 1px;
  color: var(--tk-ink);
}
.pick-opt .pn {
  display: block;
  margin-top: 4px;
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
  line-height: 1.6;
}

/* 提示条（浅底 + 左色条一族：直角） */
.wm-note {
  margin-top: 16px;
  padding: 11px 14px;
  border-left: 3px solid var(--tk-brand);
  background: var(--tk-blue-soft);
  font-size: 12px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  line-height: 1.7;
}

@media (max-width: 640px) {
  .pick-grid {
    grid-template-columns: 1fr;
  }
}

/* 克制过渡（hover 上浮 180ms）；入场沿用全局 .fade-up */
.pick-opt {
  transition:
    transform 0.18s var(--tk-ease),
    box-shadow 0.18s var(--tk-ease);
}

@media (prefers-reduced-motion: reduce) {
  .wm-hero,
  .flow-card,
  .pick-grid,
  .wm-note {
    animation: none;
  }
  .pick-opt {
    transition: none;
  }
  .pick-opt:hover {
    transform: none;
  }
}
</style>
