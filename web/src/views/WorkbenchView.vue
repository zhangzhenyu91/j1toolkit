<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">工作台</h1>
      <span class="page-sub">应用入口与快捷导航</span>
    </div>

    <t-loading :loading="loading" size="small">
      <div class="app-grid">
        <div v-for="app in apps" :key="app.code" class="app-card">
          <div class="app-icon">{{ (app.name || '?').slice(0, 1) }}</div>
          <div class="app-body">
            <div class="app-name">{{ app.name }}</div>
            <div class="app-desc">{{ app.description }}</div>
            <div class="app-foot">
              <span class="dot-tag" :class="app.tagColor || 'blue'">
                <span class="dot"></span>{{ app.tag }}
              </span>
              <div class="spacer"></div>
              <t-button size="small" theme="primary" :disabled="!app.route" @click="enterApp(app)">
                {{ app.route ? '进入应用' : '小程序使用' }}
              </t-button>
            </div>
          </div>
        </div>
      </div>
      <div v-if="!loading && !apps.length" class="empty-hint">暂无可用应用</div>
    </t-loading>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue';
import { useRouter } from 'vue-router';
import { MessagePlugin } from 'tdesign-vue-next';
import { isAdmin, listApps } from '../api.js';
import { canonicalKey, metaForApp, routeForApp } from '../features/core/app-catalog.js';

const router = useRouter();
const apps = ref([]);
const loading = ref(false);

// 工作台应用列表：按后端 GET /api/v1/app/list（按权限下发）展示，不写死；
// 本地目录只补接口不返回的落地路由与标签/描述兜底（见 features/core/app-catalog.js）。
// 微信智能应答/应用日志为管理入口，仅超级管理员可见（不进工作台卡片）。
onMounted(async () => {
  loading.value = true;
  try {
    const list = await listApps();
    apps.value = list
      .map((a) => {
        const code = canonicalKey(a.code || a.app_key || '');
        const meta = metaForApp(code);
        return {
          code,
          name: a.name || (meta && meta.name) || code,
          description: a.description || (meta && meta.desc) || '',
          tag: (meta && meta.tag) || (a.enabled === false ? '已停用' : '已接入'),
          tagColor: (meta && meta.tagColor) || (a.enabled === false ? 'red' : 'green'),
          route: routeForApp(code),
          adminOnly: !!(meta && meta.adminOnly),
        };
      })
      .filter((a) => !a.adminOnly || isAdmin());
  } catch (err) {
    // 后端返回的错误原样展示（依赖未接入时为「应用目录未接入…」等真实信息）
    MessagePlugin.error(err.message || '应用列表加载失败');
  } finally {
    loading.value = false;
  }
});

function enterApp(app) {
  if (app.route) {
    router.push(app.route);
    return;
  }
  MessagePlugin.info(`「${app.name}」为小程序能力，暂无 Web 端`);
}
</script>

<style scoped>
.empty-hint {
  padding: 32px 0;
  text-align: center;
  color: var(--td-text-color-placeholder, #86909c);
}
</style>
