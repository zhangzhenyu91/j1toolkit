<template>
  <div class="login-wrap">
    <div class="login-card">
      <div class="login-title">
        <div class="brand-mark">壹</div>
        <span>Shade 壹匣</span>
      </div>
      <div class="login-sub">班组数字化工具平台 · 管理台</div>

      <t-form layout="vertical" @submit.prevent="onSubmit">
        <t-form-item label="账号">
          <t-input v-model="username" placeholder="请输入账号" autocomplete="username" />
        </t-form-item>
        <t-form-item label="密码">
          <t-input
            v-model="password"
            type="password"
            placeholder="请输入密码"
            autocomplete="current-password"
          />
        </t-form-item>
        <t-button block theme="primary" :loading="loading" @click="onSubmit">登录</t-button>
      </t-form>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { MessagePlugin } from 'tdesign-vue-next';
import { login } from '../api.js';
import { normalizeLegacyPath } from '../features/core/legacy-urls.js';

const route = useRoute();
const router = useRouter();
const username = ref('');
const password = ref('');
const loading = ref(false);

// 登录：调 Go /api/v1/auth/login，成功后进目标页（旧 *.html 回跳地址归一为 SPA 路由）；
// 失败把服务端返回的错误信息真实展示（如「账号或密码不正确」）。
async function onSubmit() {
  if (!username.value || !password.value) {
    MessagePlugin.warning('请输入账号与密码');
    return;
  }
  loading.value = true;
  try {
    await login({ username: username.value, password: password.value });
    MessagePlugin.success('登录成功');
    const redirect = route.query.redirect;
    const target = Array.isArray(redirect) ? redirect[0] : redirect;
    router.push(typeof target === 'string' && target ? normalizeLegacyPath(target) : '/');
  } catch (err) {
    MessagePlugin.error(err.message || '登录失败');
  } finally {
    loading.value = false;
  }
}
</script>
