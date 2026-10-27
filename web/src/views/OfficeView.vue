<template>
  <!-- ONLYOFFICE 承载页：纯 OO 满铺（无 Shade 壳内容），就绪前品牌加载层 -->
  <div class="oo-page">
    <div ref="holderEl" class="oo-holder" :class="{ on: ready }"></div>
    <div v-if="state !== 'done'" class="oo-state" :class="{ out: state === 'ready' }">
      <div v-if="state === 'loading'" class="oo-ld">
        <div class="oo-ld-mk">壹</div>
        <div class="oo-ld-name">{{ docName }}</div>
        <div class="oo-ld-bar"><i></i></div>
        <div class="oo-ld-t">正在加载编辑器…</div>
      </div>
      <div v-else class="oo-err">
        <div class="oo-err-ic">!</div>
        <p>{{ errMsg }}</p>
        <t-button theme="primary" @click="retry">重新加载</t-button>
      </div>
    </div>
  </div>
</template>

<script setup>
// 在线编辑/预览承载页（迁移 server/public/office.html）：
// query 除导航参数 back/name 外全部透传 POST /api/v1/office/config（网盘 space/path/mode、
// 安全日 src=safeday&id= 等多源形态）；返回 = back 来源页（仅放行 '/' 开头站内路径），
// OO 右上角「关闭文件」同口径；mode=view 为仅预览（后端对仅预览格式与非网盘源仍强制 view）。
import { onBeforeUnmount, onMounted, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { getConfig, editorBaseUrl, loadDocsScript, isDemo } from '../features/office/api.js';

const route = useRoute();
const router = useRouter();

const holderEl = ref(null);
const state = ref('loading'); // loading / ready / done / error
const ready = ref(false);
const errMsg = ref('');

// 文档名：query.name 优先，缺省取 path 末段（与旧页一致）
const body = {};
Object.entries(route.query).forEach(([k, v]) => {
  if (k !== 'back' && k !== 'name') body[k] = Array.isArray(v) ? v[0] : v;
});
body.mode = body.mode === 'view' ? 'view' : 'edit';
const docName =
  route.query.name ||
  String(body.path || '')
    .split('/')
    .filter(Boolean)
    .pop() ||
  '文档';

let editor = null;
let destroyed = false;

onMounted(() => {
  document.title = `${docName} · ${body.mode === 'view' ? '在线预览' : '在线编辑'} · Shade 壹匣`;
  boot();
});

onBeforeUnmount(() => {
  destroyed = true;
  document.title = 'Shade 壹匣';
});

// back 返回来源页：仅放行 '/' 开头站内路径（防 open redirect，//host 与 /\host 拦截），
// 缺省回团队网盘；.html 后缀归一为 Vue 路由路径（如 /netdisk.html → /netdisk）
function back() {
  let target = route.query.back || '';
  if (Array.isArray(target)) target = target[0] || '';
  const safe = target.charAt(0) === '/' && target.charAt(1) !== '/' && target.charAt(1) !== '\\';
  const dest = safe ? target : '/netdisk';
  const path = dest.replace(/\.html$/, '');
  router.push(path);
}

function failState(msg) {
  if (destroyed) return;
  errMsg.value = msg;
  state.value = 'error';
}

async function boot() {
  state.value = 'loading';
  ready.value = false;
  errMsg.value = '';
  try {
    const data = await getConfig(body);
    const publicUrl = editorBaseUrl(data);
    if (!publicUrl || !data.config) throw new Error('编辑器配置下发异常');
    await loadDocsScript(publicUrl);
    if (destroyed) return;
    const cfg = { ...data.config };
    cfg.width = '100%';
    cfg.height = '100%';
    cfg.events = {
      // 文档渲染就绪：加载层淡出 + 编辑器上浮揭示（一次编排，落位即静止）
      onDocumentReady: () => {
        state.value = 'ready';
        ready.value = true;
        setTimeout(() => {
          if (!destroyed) state.value = 'done';
        }, 320);
      },
      onRequestClose: back,
      onError: (e) => {
        console.error('[OO] 编辑器错误：', e);
        failState('编辑器错误：' + ((e && e.data && e.data.errorDescription) || '未知错误'));
      },
    };
    // DocsAPI.DocEditor 由动态脚本挂到 window（全局副作用，无法静态导入）
    editor = new window.DocsAPI.DocEditor(holderEl.value, cfg);
  } catch (e) {
    if (destroyed) return;
    failState(e.message || '加载失败，请稍后重试');
  }
}

function retry() {
  if (editor) {
    try {
      editor.destroyEditor();
    } catch (e) {
      /* 忽略销毁异常 */
    }
    editor = null;
  }
  // 整页重载最稳（动态脚本与 OO 实例均可能已污染）
  location.reload();
}

// 演示适配层不下发编辑器配置（boot 内 getConfig 会报错），此处仅提前给出明确口径
if (isDemo()) {
  // 不拦截加载，由 getConfig 的拒绝信息统一呈现
}
</script>

<style scoped>
.oo-page {
  position: relative;
  height: 100%;
  min-height: 420px;
  background: #fff;
}
.oo-holder {
  position: absolute;
  inset: 0;
  opacity: 0;
  transform: translateY(10px);
  transition:
    opacity 0.45s cubic-bezier(0.22, 1, 0.36, 1),
    transform 0.45s cubic-bezier(0.22, 1, 0.36, 1);
}
.oo-holder.on {
  opacity: 1;
  transform: none;
}
.oo-state {
  position: absolute;
  inset: 0;
  z-index: 2;
  background: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--tk-ph);
  font-size: 13px;
  transition: opacity 0.3s ease;
}
.oo-state.out {
  opacity: 0;
  pointer-events: none;
}
.oo-ld {
  text-align: center;
  animation: oo-ld-in 0.5s cubic-bezier(0.22, 1, 0.36, 1) both;
}
.oo-ld-mk {
  width: 46px;
  height: 46px;
  margin: 0 auto 14px;
  border-radius: 12px;
  background: var(--tk-brand);
  color: #fff;
  font-size: 20px;
  font-weight: 700;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 8px 20px rgba(14, 61, 168, 0.22);
}
.oo-ld-name {
  max-width: min(420px, 80vw);
  margin: 0 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
  font-weight: 600;
  color: var(--tk-ink);
}
.oo-ld-bar {
  width: 168px;
  height: 3px;
  margin: 14px auto 10px;
  border-radius: 2px;
  background: var(--tk-line);
  overflow: hidden;
  position: relative;
}
.oo-ld-bar i {
  position: absolute;
  top: 0;
  left: -40%;
  width: 40%;
  height: 100%;
  border-radius: 2px;
  background: var(--tk-yellow);
  animation: oo-sweep 1.1s cubic-bezier(0.45, 0, 0.55, 1) infinite;
}
.oo-ld-t {
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
}
.oo-err {
  text-align: center;
  max-width: 420px;
  line-height: 1.9;
  padding: 0 20px;
}
.oo-err-ic {
  width: 40px;
  height: 40px;
  margin: 0 auto 10px;
  border-radius: 50%;
  background: var(--tk-yellow-soft);
  color: #d25f00;
  font-size: 22px;
  font-weight: 700;
  display: flex;
  align-items: center;
  justify-content: center;
}
.oo-err p {
  margin: 0 0 12px;
  color: var(--tk-sub);
}
@keyframes oo-ld-in {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@keyframes oo-sweep {
  to {
    left: 100%;
  }
}
@media (prefers-reduced-motion: reduce) {
  .oo-holder {
    opacity: 1 !important;
    transform: none !important;
    transition: none;
  }
  .oo-ld {
    animation: none;
  }
  .oo-ld-bar i {
    animation: none;
  }
  .oo-state {
    transition: none;
  }
}
</style>
