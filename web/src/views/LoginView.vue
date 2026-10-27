<template>
  <!-- 旧版登录页：左实景品牌区（班组作业实景 + 深蓝渐变遮罩）+ 右账号密码表单 + 协议/备案/客户端推广区 -->
  <div class="login-page">
    <div class="login-box">
      <!-- 左侧品牌区（深蓝底 + 极淡网格线） -->
      <div class="login-brand">
        <div class="brand-top">
          <span class="logo-mark"><img src="/toolkit.svg" alt="Shade 壹匣" /></span>
          <div class="brand-name">Shade 壹匣<small>班组数字化工具平台</small></div>
        </div>
        <div class="brand-mid">
          <h2>班组应用，<br />一匣尽纳，<br /><em>安全每一班。</em></h2>
          <p>统一登录 · 应用聚合 · 权限管控，班组的一站式数字化工作台。</p>
          <div class="brand-feats">
            <div class="feat"><i><LegacyIcon name="user" :size="17" /></i>统一身份认证，班组账号一键登录</div>
            <div class="feat"><i><LegacyIcon name="app" :size="17" /></i>应用中心聚合，业务应用按权限自动下发</div>
            <div class="feat"><i><LegacyIcon name="shield" :size="17" /></i>数据安全可溯，操作留痕符合班组管理规范</div>
          </div>
        </div>
      </div>

      <!-- 右侧登录表单：.form-core 垂直居中主体，备案页脚垫底 -->
      <form class="login-form" novalidate @submit.prevent="onSubmit">
        <div class="form-core">
          <h2>欢迎回来</h2>
          <div class="sub">请使用班组账号登录 Shade 壹匣工作台</div>
          <div class="field" id="fUser">
            <LegacyIcon name="user" :size="18" />
            <input
              v-model="username"
              class="input"
              type="text"
              placeholder="请输入账号"
              autocomplete="username"
            />
          </div>
          <div class="field" id="fPass">
            <LegacyIcon name="lock-on" :size="18" />
            <input
              v-model="password"
              class="input"
              type="password"
              placeholder="请输入密码"
              autocomplete="current-password"
            />
          </div>
          <div class="form-row">
            <label class="ck-l">
              <input v-model="remember" type="checkbox" hidden />
              <span class="ck" :class="{ on: remember }"><LegacyIcon name="check" :size="10" /></span
              >记住账号
            </label>
            <a href="javascript:void 0" @click="onForgot">忘记密码？</a>
          </div>
          <button class="btn btn-primary btn-block" type="submit" :disabled="loading">
            {{ loading ? '登录中…' : '登 录' }}
          </button>
          <div class="agree">
            登录即代表已阅读并同意<a href="javascript:void 0" @click="onUla">《用户协议》</a
            >和<a href="javascript:void 0" @click="onPp">《隐私政策》</a>
          </div>
          <div class="login-foot">
            <div>© 2026 Shade 壹匣</div>
          </div>
        </div>

        <!-- 右侧表单底部推广区：小程序宣传图 + 内网客户端下载卡 -->
        <div class="form-promo">
          <img src="/mp-promo.jpg" alt="微信搜一搜「Shade 壹匣」小程序" />
          <a class="promo-client" :href="promoHref" target="_blank" rel="noopener">
            <span class="pc-ic"><img src="/assets/cnb.png" alt="CNB" /></span>
            <span class="pc-tx">
              <b>内网客户端</b>
              <small>内网被控机安装包（Windows / 麒麟）</small>
              <span class="pc-ver">{{ promoVer }}</span>
            </span>
            <span class="pc-arrow"><LegacyIcon name="arrow-right" :size="16" /></span>
          </a>
        </div>
        <footer class="beian-foot">
          <a href="https://beian.miit.gov.cn/" target="_blank" rel="noopener">晋ICP备2025063709号-2</a>
          <a
            href="https://beian.mps.gov.cn/#/query/webSearch?code=14118202000050"
            target="_blank"
            rel="noopener"
            ><img src="/public-security.png" alt="公安备案" />晋公网安备14118202000050号</a
          >
        </footer>
      </form>
    </div>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { MessagePlugin } from 'tdesign-vue-next';
import { isLoggedIn, login } from '../api.js';
import { normalizeLegacyPath } from '../features/core/legacy-urls.js';
import LegacyIcon from '../features/core/LegacyIcon.vue';

const route = useRoute();
const router = useRouter();
const username = ref('');
const password = ref('');
const remember = ref(false);
const loading = ref(false);

// 内网客户端推广卡：CNB Releases 最新 tag（GitHub API 拉最新 tag 拼接；1h 本地缓存，失败保持列表页）
const CNB_BASE = 'https://cnb.cool/j1net/j1toolkit/j1toolkit/-/releases';
const promoTag = ref('');
const promoHref = ref(CNB_BASE);
const promoVer = ref('前往 CNB 下载');

function applyPromoTag(tag) {
  if (!tag) return;
  promoTag.value = tag;
  promoHref.value = CNB_BASE + '/tag/' + encodeURIComponent(tag);
  promoVer.value = '下载最新版 ' + tag;
}

// 登录：调 Go /api/v1/auth/login（client:'web' 签短时效 token），成功后进目标页
// （旧 *.html 回跳地址归一为 SPA 路由）；失败把服务端返回的错误信息真实展示。
async function onSubmit() {
  const uname = username.value.trim();
  if (!uname || !password.value) {
    MessagePlugin.warning('请输入账号和密码');
    return;
  }
  loading.value = true;
  try {
    await login({ username: uname, password: password.value });
    // 记住账号：回显上次登录的账号
    try {
      if (remember.value) localStorage.setItem('shade_remember', uname);
      else localStorage.removeItem('shade_remember');
    } catch (e) {
      // localStorage 不可用时忽略
    }
    MessagePlugin.success('登录成功，正在进入工作台');
    const redirect = route.query.redirect;
    const target = Array.isArray(redirect) ? redirect[0] : redirect;
    const dest = typeof target === 'string' && target ? normalizeLegacyPath(target) : '/';
    setTimeout(() => router.push(dest), 300);
  } catch (err) {
    MessagePlugin.error(err.message || '登录失败，请稍后重试');
  } finally {
    loading.value = false;
  }
}

function onForgot() {
  MessagePlugin.info('请联系管理员重置密码');
}
function onUla() {
  MessagePlugin.info('用户协议由班组管理员发布，请遵守平台使用规范');
}
function onPp() {
  MessagePlugin.info('平台数据仅用于班组内部业务管理');
}

onMounted(async () => {
  // 已登录直接进入工作台（与旧版登录页同行为）
  if (isLoggedIn()) {
    router.replace('/');
    return;
  }
  // 记住账号：回显上次登录的账号
  try {
    const remembered = localStorage.getItem('shade_remember') || '';
    if (remembered) {
      username.value = remembered;
      remember.value = true;
    }
  } catch (e) {
    // localStorage 不可用时忽略
  }
  // 客户端推广卡版本号：本地缓存 1h 内直接回显，再拉 GitHub 最新 tag 校正（失败保持列表页链接）
  try {
    const cached = JSON.parse(localStorage.getItem('shade_latest_tag') || 'null');
    if (cached && cached.tag && Date.now() - cached.at < 3600000) applyPromoTag(cached.tag);
  } catch (e) {
    // 缓存损坏忽略
  }
  try {
    const r = await fetch('https://api.github.com/repos/zhangzhenyu91/j1toolkit/releases/latest');
    if (!r.ok) return;
    const j = await r.json();
    if (j && j.tag_name) {
      applyPromoTag(j.tag_name);
      localStorage.setItem('shade_latest_tag', JSON.stringify({ tag: j.tag_name, at: Date.now() }));
    }
  } catch (e) {
    // 拉取失败保持 releases 列表页链接
  }
});
</script>
