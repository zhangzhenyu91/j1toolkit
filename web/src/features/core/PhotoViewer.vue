<template>
  <!-- 照片灯箱（树障辅证照片 / 网盘图片预览共用）：t-image-viewer 居中灯箱口径——
       点图片外蒙层退出、点图片本身不关，保留关闭按钮与键盘 Esc；自带缩放工具与适应缩放、深浅主题通用。
       onDownload 可选：父级传入时接管工具栏下载按钮，缺省交回 t-image-viewer 默认下载 -->
  <t-image-viewer
    :visible="visible"
    :images="images"
    :index="index"
    :title="pill"
    :trigger="renderEmptyTrigger"
    :close-on-overlay="true"
    :on-download="onDownload"
    @update:visible="emit('update:visible', $event)"
    @update:index="emit('update:index', $event)"
    @close="emit('update:visible', false)"
  />
</template>

<script setup>
import { computed } from 'vue';

const props = defineProps({
  // 是否显示
  visible: { type: Boolean, default: false },
  // 图片列表（URL 字符串，或 t-image-viewer ImageInfo 对象）
  images: { type: Array, default: () => [] },
  // 当前序号（受控，配合 v-model:index，保证每次打开定位到目标张）
  index: { type: Number, default: 0 },
  // 可选标题（如文件名）：有值时序号胶囊显示「标题（i/n）」，缺省为组件默认「i/n」
  title: { type: String, default: '' },
  // 可选下载拦截（收到当前图片 URL）
  onDownload: { type: Function, default: undefined },
});

const emit = defineEmits(['update:visible', 'update:index']);

// 禁用默认触发图：trigger 传空渲染函数，不再在目录里生成 images[0] 触发块
function renderEmptyTrigger() {
  return null;
}

// pill 序号胶囊文案：带 title 时叠加文件名，否则留空交回组件默认序号
const pill = computed(() => {
  if (!props.title) return undefined;
  const total = props.images.length;
  return `${props.title}（${(props.index || 0) + 1}/${total}）`;
});
</script>

<style>
/* 查看器悬浮圆钮/序号胶囊压在常暗蒙层上，取色 --td-text-color-anti 却随系统深色翻转为深色，
   深色主题下深字压暗底不可读；这里固定浅色，深浅主题一致可读 */
.t-image-viewer-preview-image .t-image-viewer__modal-index {
  color: #fff;
  max-width: min(72vw, 720px);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.t-image-viewer-preview-image .t-image-viewer__modal-icon.t-image-viewer__modal-close-bt,
.t-image-viewer-preview-image .t-image-viewer__modal-icon.t-image-viewer__modal-prev-bt,
.t-image-viewer-preview-image .t-image-viewer__modal-icon.t-image-viewer__modal-next-bt {
  color: #fff;
}
/* 禁用态箭头保持半透明弱化（避免被上面固定浅色盖掉） */
.t-image-viewer-preview-image .t-image-viewer__modal-icon.t-is-disabled {
  color: rgba(255, 255, 255, 0.22);
}
</style>
