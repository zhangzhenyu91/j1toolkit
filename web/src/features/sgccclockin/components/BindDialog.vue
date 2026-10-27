<template>
  <div v-if="open" class="wl-modal-mask" @click.self="close">
    <div class="wl-modal card sg-bind-modal">
      <h3 class="card-title">商旅账号</h3>
      <div class="sub-text">绑定后可在出工日志发起打卡 / 同步费用；登录态过期可在此重新登录</div>

      <!-- 当前绑定状态 -->
      <div v-if="accLoading" class="muted sg-bind-load">账号状态加载中…</div>
      <template v-else-if="account && account.bound">
        <div class="sg-bind-rows">
          <div class="sg-bind-row">
            <span class="sg-bind-k">手机号</span>
            <span class="sg-bind-v num">{{ account.mobile_masked }}</span>
          </div>
          <div class="sg-bind-row">
            <span class="sg-bind-k">登录状态</span>
            <span class="dot-tag" :class="account.token_status === 1 ? 'green' : 'red'">
              <span class="dot"></span>{{ account.token_status === 1 ? '正常' : '已过期' }}
            </span>
          </div>
          <div class="sg-bind-row">
            <span class="sg-bind-k">设备型号</span>
            <span class="sg-bind-v">{{ account.device_type || '—' }}</span>
          </div>
          <div class="sg-bind-row">
            <span class="sg-bind-k">系统版本</span>
            <span class="sg-bind-v">{{ account.system_version || '—' }}</span>
          </div>
        </div>
        <div class="sg-bind-dev">
          <input v-model="deviceType" type="text" class="wl-range" placeholder="设备型号" />
          <input v-model="systemVersion" type="text" class="wl-range" placeholder="系统版本" />
          <t-button size="small" variant="outline" :loading="devSaving" @click="saveDevice">
            保存设备口径
          </t-button>
        </div>
        <div class="sg-bind-actions">
          <t-button size="small" variant="outline" @click="relogin">重新登录</t-button>
          <t-button
            size="small"
            theme="danger"
            variant="outline"
            :loading="unbinding"
            @click="unbind"
          >
            解绑
          </t-button>
        </div>
      </template>

      <!-- 绑定表单 -->
      <template v-else>
        <div class="sg-bind-rows">
          <div class="sg-bind-row">
            <span class="sg-bind-k">手机号</span>
            <input
              v-model="form.mobile"
              type="tel"
              maxlength="11"
              class="wl-range num"
              placeholder="商旅平台登录手机号"
            />
          </div>
        </div>

        <!-- 风险验证：图形码 / 滑块（40035 自动降级滑块） -->
        <div class="sg-bind-risk">
          <div class="sg-bind-risk-head">
            <span class="field-label">安全验证</span>
            <button
              v-if="!sliderOn"
              type="button"
              class="wl-act"
              :disabled="captchaLoading"
              @click="loadCaptcha"
            >
              {{ captchaImage ? '刷新图形验证码' : '获取图形验证码' }}
            </button>
            <button v-else type="button" class="wl-act" @click="switchToCaptcha">
              改用图形验证码
            </button>
          </div>
          <template v-if="!sliderOn">
            <img v-if="captchaImage" :src="captchaImage" class="sg-captcha-img" alt="图形验证码" />
            <input
              v-if="captchaImage"
              v-model="form.checkImgCode"
              type="text"
              maxlength="8"
              class="wl-range num"
              placeholder="输入图形验证码"
            />
          </template>
          <template v-else>
            <iframe
              v-if="sliderOn"
              ref="sliderFrame"
              class="sg-slider-frame"
              src="/sgcc-captcha.html?from=web"
              title="安全验证 · 商旅平台"
            ></iframe>
            <div class="sg-bind-tip" :class="{ ok: !!form.captchaToken }">
              {{ form.captchaToken ? '滑块验证已完成' : '请在上方完成滑块验证' }}
            </div>
          </template>
        </div>

        <div class="sg-bind-actions">
          <t-button size="small" variant="outline" :loading="smsSending" @click="sendSms">
            发送短信
          </t-button>
        </div>

        <!-- 短信码 / 密码 二选一完成绑定 -->
        <div class="sg-bind-rows">
          <div class="sg-bind-row">
            <span class="sg-bind-k">短信验证码</span>
            <input
              v-model="form.checkCode"
              type="text"
              maxlength="8"
              class="wl-range num"
              placeholder="短信验证码"
            />
          </div>
          <div class="sg-bind-row">
            <span class="sg-bind-k">或 密码</span>
            <input
              v-model="form.password"
              type="password"
              class="wl-range"
              placeholder="商旅平台登录密码"
            />
          </div>
        </div>
        <div class="sg-bind-tip">短信验证码与密码二选一；密码登录需先完成安全验证</div>

        <div class="sg-bind-actions">
          <t-button :loading="binding" @click="bind">完成绑定</t-button>
        </div>
      </template>

      <div v-if="error" class="wl-error">{{ error }}</div>
      <div class="wl-modal-foot">
        <t-button variant="outline" @click="close">关闭</t-button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onBeforeUnmount, reactive, ref, watch } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import {
  bindAccount,
  fetchAccount,
  loginCaptcha,
  loginSms,
  unbindAccount,
  updateAccountDevice,
} from '../api.js';

// 商旅账号绑定弹层：本人绑定状态 + 图形码/滑块双通道安全验证 + 短信码/密码登录。
// 滑块走 /sgcc-captcha.html iframe 接力（message 事件 type=sgcc-captcha 回传 captchaToken+constId）。
const props = defineProps({
  open: { type: Boolean, default: false },
});
const emit = defineEmits(['update:open', 'bound']);

const accLoading = ref(false);
const account = ref(null);
const error = ref('');
const deviceType = ref('');
const systemVersion = ref('');
const devSaving = ref(false);
const unbinding = ref(false);
const captchaLoading = ref(false);
const captchaImage = ref('');
const sliderOn = ref(false);
const smsSending = ref(false);
const binding = ref(false);
const form = reactive({
  mobile: '',
  checkImgCode: '',
  checkCode: '',
  password: '',
  captchaToken: '',
  constId: '',
});

// onMessage 滑块接力回传（仅认本源 sgcc-captcha 消息）。
function onMessage(ev) {
  if (!ev.data || ev.data.type !== 'sgcc-captcha') return;
  if (ev.origin !== window.location.origin) return;
  form.captchaToken = ev.data.captchaToken || '';
  form.constId = ev.data.constId || '';
}
window.addEventListener('message', onMessage);
onBeforeUnmount(() => window.removeEventListener('message', onMessage));

// open 变化时拉本人绑定状态。
watch(
  () => props.open,
  async (val) => {
    if (!val) return;
    error.value = '';
    accLoading.value = true;
    try {
      account.value = await fetchAccount();
      deviceType.value = (account.value && account.value.device_type) || '';
      systemVersion.value = (account.value && account.value.system_version) || '';
    } catch (e) {
      account.value = null;
      error.value = e.message || '账号状态加载失败';
    } finally {
      accLoading.value = false;
    }
  }
);

// loadCaptcha 取图形验证码；40035（风控窗口期）自动降级滑块。
async function loadCaptcha() {
  if (!form.mobile) {
    error.value = '请先填写手机号';
    return;
  }
  error.value = '';
  captchaLoading.value = true;
  try {
    const data = await loginCaptcha({ mobile: form.mobile });
    captchaImage.value = data.image || '';
    sliderOn.value = false;
    form.checkImgCode = '';
    form.captchaToken = '';
    form.constId = '';
    if (!captchaImage.value) switchToSlider();
  } catch (e) {
    if (e.code === 40035) {
      switchToSlider();
    } else {
      error.value = e.message || '图形验证码获取失败';
    }
  } finally {
    captchaLoading.value = false;
  }
}

// switchToSlider 降级滑块（顶象接力页）。
function switchToSlider() {
  sliderOn.value = true;
  captchaImage.value = '';
  form.checkImgCode = '';
  form.captchaToken = '';
  form.constId = '';
}

// switchToCaptcha 切回图形码通道。
function switchToCaptcha() {
  switchToSlider();
  sliderOn.value = false;
  loadCaptcha();
}

// sendSms 发短信（图形码 v2 / 滑块 v3 双通道）。
async function sendSms() {
  if (!form.mobile) {
    error.value = '请先填写手机号';
    return;
  }
  error.value = '';
  smsSending.value = true;
  try {
    await loginSms({
      mobile: form.mobile,
      checkImgCode: form.checkImgCode,
      captchaToken: form.captchaToken,
      constId: form.constId,
    });
    MessagePlugin.success('短信已发送');
  } catch (e) {
    if (e.code === 40035 && !sliderOn.value) {
      switchToSlider();
      error.value = '图形验证码暂不可用，请完成滑块验证后重试';
    } else {
      error.value = e.message || '短信发送失败';
    }
  } finally {
    smsSending.value = false;
  }
}

// bind 完成绑定（短信码或密码换 token）。
async function bind() {
  if (!form.mobile) {
    error.value = '请先填写手机号';
    return;
  }
  if (!form.checkCode && !form.password) {
    error.value = '请填写短信验证码或密码';
    return;
  }
  error.value = '';
  binding.value = true;
  try {
    await bindAccount({
      mobile: form.mobile,
      checkCode: form.checkCode,
      password: form.password,
      checkImgCode: form.checkImgCode,
      captchaToken: form.captchaToken,
      constId: form.constId,
    });
    MessagePlugin.success('绑定成功');
    emit('bound');
    try {
      account.value = await fetchAccount();
    } catch (e) {
      account.value = null;
    }
  } catch (e) {
    error.value = e.message || '绑定失败';
  } finally {
    binding.value = false;
  }
}

// saveDevice 保存设备口径（打卡时按绑定机型上送）。
async function saveDevice() {
  error.value = '';
  devSaving.value = true;
  try {
    await updateAccountDevice({ deviceType: deviceType.value, systemVersion: systemVersion.value });
    MessagePlugin.success('设备口径已保存');
  } catch (e) {
    error.value = e.message || '保存失败';
  } finally {
    devSaving.value = false;
  }
}

// relogin 清空绑定态展示、回到绑定表单重新登录（不动原绑定，登录成功即覆盖 token）。
function relogin() {
  account.value = null;
  error.value = '';
  form.checkCode = '';
  form.password = '';
  form.checkImgCode = '';
  form.captchaToken = '';
  form.constId = '';
  captchaImage.value = '';
  sliderOn.value = false;
}

// unbind 解绑（确认后调 API）。
function unbind() {
  const dlg = DialogPlugin.confirm({
    header: '解绑商旅账号',
    body: '解绑后将无法发起打卡与费用同步，确定解绑？',
    confirmBtn: '解绑',
    onConfirm: async () => {
      error.value = '';
      unbinding.value = true;
      try {
        await unbindAccount();
        MessagePlugin.success('已解绑');
        account.value = { bound: false };
        emit('bound');
      } catch (e) {
        error.value = e.message || '解绑失败';
      } finally {
        unbinding.value = false;
        dlg.destroy();
      }
    },
  });
}

function close() {
  emit('update:open', false);
}
</script>

<style scoped>
.sg-bind-modal {
  width: min(520px, calc(100vw - 32px));
}
.sg-bind-load {
  padding: 18px 0;
  text-align: center;
}
.sg-bind-rows {
  margin: 10px 0;
}
.sg-bind-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 5px 0;
}
.sg-bind-k {
  width: 90px;
  flex-shrink: 0;
  font-size: 13px;
  color: var(--tk-sub);
}
.sg-bind-v {
  font-size: 13px;
  color: var(--tk-ink);
}
.sg-bind-row .wl-range {
  flex: 1;
}
.sg-bind-dev {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin: 8px 0;
}
.sg-bind-dev .wl-range {
  flex: 1;
  min-width: 130px;
}
.sg-bind-actions {
  display: flex;
  gap: 10px;
  margin: 10px 0;
}
.sg-bind-risk {
  background: var(--tk-fill);
  padding: 10px;
  margin: 10px 0;
}
.sg-bind-risk-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.sg-captcha-img {
  height: 40px;
  border: 1px solid var(--tk-line);
  border-radius: 4px;
  display: block;
  margin-bottom: 8px;
  cursor: pointer;
}
.sg-slider-frame {
  width: 100%;
  height: 178px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
}
.sg-bind-tip {
  margin-top: 8px;
  font-size: 12px;
  color: var(--tk-sub);
}
.sg-bind-tip.ok {
  color: #0b8a37;
}
.wl-error {
  margin: 8px 0;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}
.wl-modal-mask {
  position: fixed;
  inset: 0;
  background: rgba(29, 33, 41, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.wl-modal {
  max-height: calc(100vh - 64px);
  overflow: auto;
}
.wl-modal-foot {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
.wl-act {
  border: none;
  background: none;
  padding: 0;
  color: var(--tk-brand);
  font-size: 12px;
  cursor: pointer;
}
.wl-range {
  height: 32px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 0 8px;
  font-size: 13px;
  color: var(--tk-ink);
  background: #fff;
}
</style>
