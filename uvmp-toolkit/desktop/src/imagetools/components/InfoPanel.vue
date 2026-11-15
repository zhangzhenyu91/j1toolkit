<template>
  <div class="it-info">
    <div class="it-ins-head">
      <b>图片信息</b>
      <span class="it-ins-tags">
        <span class="it-tag is-info">{{ formatLabel }}</span>
        <button
          type="button"
          class="it-iconbtn it-ins-x"
          title="收起信息栏"
          aria-label="收起信息栏"
          @click="$emit('close')"
        >
          <ITIcon name="close" :size="15" />
        </button>
      </span>
    </div>

    <div class="it-ins-body">
      <div class="it-nameblock">
        <span class="it-ftype">{{ extLabel }}</span>
        <div class="it-namewrap">
          <span class="it-flabel">当前文件</span>
          <div class="it-fname" :title="file.name">{{ file.name }}</div>
          <div class="it-fmeta">照片素材 · {{ index + 1 }} / {{ total }}</div>
        </div>
      </div>

      <dl class="it-meta">
        <div class="it-mrow">
          <dt>名称</dt>
          <dd class="it-mono">{{ file.name }}</dd>
        </div>
        <div class="it-mrow">
          <dt>类型</dt>
          <dd>
            <span>{{ formatLabel }} 图像</span>
            <span class="it-sub">{{ file.mime || 'image/*' }}</span>
          </dd>
        </div>
        <div class="it-mrow">
          <dt>大小</dt>
          <dd class="it-num">
            <span>{{ sizeText }}</span>
            <span class="it-sub">{{ bytesText }}</span>
          </dd>
        </div>
        <div class="it-mrow">
          <dt>尺寸</dt>
          <dd class="it-num">
            <span>{{ pixelsText }}</span>
            <span class="it-sub">{{ dimSub }}</span>
          </dd>
        </div>
        <div class="it-mrow it-pathrow">
          <dt>位置</dt>
          <dd>
            <div class="it-pathwrap">
              <button
                type="button"
                class="it-pathbtn"
                :title="file.path"
                @click="pathOpen = !pathOpen"
              >
                <span class="it-path-text">{{ file.path }}</span>
              </button>
              <button
                type="button"
                class="it-iconbtn is-tiny"
                title="复制完整路径"
                aria-label="复制完整路径"
                @click="copyPath"
              >
                <ITIcon name="copy" :size="13" />
              </button>
            </div>
            <span class="it-sub">完整路径，长路径自动换行；复制或点开弹层取全文</span>
            <div v-if="pathOpen" class="it-pathpop" role="dialog" aria-label="完整路径">
              <div class="it-pop-head">
                <b>完整路径</b>
                <button
                  type="button"
                  class="it-iconbtn"
                  aria-label="关闭完整路径"
                  @click="pathOpen = false"
                >
                  <ITIcon name="close" :size="14" />
                </button>
              </div>
              <input
                class="it-input is-mono"
                :value="file.path"
                readonly
                @focus="$event.target.select()"
              />
              <div class="it-pop-foot">
                <button type="button" class="it-btn is-ghost" @click="copyPath">复制路径</button>
                <button type="button" class="it-btn is-ghost" @click="$emit('open-folder')">
                  打开所在文件夹
                </button>
              </div>
            </div>
          </dd>
        </div>
        <div class="it-mrow">
          <dt>修改时间</dt>
          <dd class="it-num">
            <span>{{ mtimeText }}</span>
            <span class="it-sub">本地时间 · 本机文件系统</span>
          </dd>
        </div>
      </dl>

      <div class="it-ins-actions">
        <button type="button" class="it-btn is-ghost is-full" @click="$emit('open-folder')">
          <ITIcon name="folder" :size="15" />打开所在文件夹
        </button>
        <button type="button" class="it-btn is-ghost is-full" @click="$emit('rename')">
          <ITIcon name="pencil" :size="15" />重命名（F2）
        </button>
      </div>

      <div class="it-assoc-link">
        {{ assocText }}
        <button type="button" class="it-linkbtn" @click="$emit('request-assoc')">
          前往文件关联
        </button>
      </div>
    </div>

    <div class="it-ins-foot"><ITIcon name="info" :size="13" />信息来自本机文件 · 不读取 EXIF</div>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue';
import ITIcon from './ITIcon.vue';
import {
  formatAspect,
  formatBytesExact,
  formatMegapixels,
  formatMtime,
  formatPixels,
  formatSize,
} from '../lib/format.mjs';
import { extLabel as toExtLabel } from '../lib/process.mjs';

const props = defineProps({
  file: { type: Object, required: true },
  index: { type: Number, default: 0 },
  total: { type: Number, default: 0 },
  defaultStatus: { type: Object, default: null },
});
const emit = defineEmits(['open-folder', 'rename', 'request-assoc', 'close', 'notify']);

const pathOpen = ref(false);

const formatLabel = computed(() =>
  String(props.file.format || toExtLabel(props.file.ext) || '图片')
);
const extLabel = computed(() => toExtLabel(props.file.ext) || 'IMG');
const sizeText = computed(() => formatSize(props.file.size));
const bytesText = computed(() => formatBytesExact(props.file.size));
const pixelsText = computed(() =>
  props.file.width && props.file.height ? formatPixels(props.file.width, props.file.height) : '—'
);
const dimSub = computed(() => {
  if (!props.file.width || !props.file.height) return '未读取到像素尺寸';
  return `${formatAspect(props.file.width, props.file.height)} · ${formatMegapixels(props.file.width, props.file.height)}`;
});
const mtimeText = computed(() => formatMtime(props.file.mtime));

const assocText = computed(() => {
  const status = props.defaultStatus;
  if (status && status.supported && status.isDefault) {
    const values = Object.values(status.isDefault);
    if (values.length && values.every(Boolean)) return '已设为默认图片查看器。';
  }
  return '未设为默认图片查看器。';
});

async function copyPath() {
  const text = String(props.file.path || '');
  try {
    await navigator.clipboard.writeText(text);
    emit('notify', '已复制完整路径');
  } catch (e) {
    // file:// 下剪贴板权限可能被拒：退回选区复制，不弹错误打断浏览
    const input = document.createElement('textarea');
    input.value = text;
    input.setAttribute('readonly', 'readonly');
    input.style.position = 'fixed';
    input.style.opacity = '0';
    document.body.appendChild(input);
    input.select();
    const ok = document.execCommand('copy');
    document.body.removeChild(input);
    emit('notify', ok ? '已复制完整路径' : '复制失败，请手动选择路径');
  }
}
</script>
