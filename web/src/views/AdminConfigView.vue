<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">管理-应用配置</h1>
      <span class="page-sub">分组管理 · 敏感密钥不回显 · 仅超级管理员</span>
    </div>

    <div class="card note">
      敏感密钥只显示脱敏形状（如 ab****yz），输入框留空表示保持不变，仅提交填写了新值的项；
      普通项同样留空不改。保存立即热生效，启动级配置仍由部署环境变量提供。
    </div>

    <t-loading :loading="loading" size="small">
      <div v-if="loadError" class="card state-box error">
        <b>配置加载失败</b>
        <p>{{ loadError }}</p>
        <t-button size="small" variant="outline" style="margin-top: 12px" @click="load">
          重新加载
        </t-button>
      </div>

      <div v-else-if="configGroups.length" class="config-layout">
        <!-- 分组侧栏 -->
        <aside class="config-side card">
          <div class="side-label">通用与应用</div>
          <div
            v-for="group in configGroups"
            :key="group.group"
            class="config-nav"
            :class="{ on: currentGroup === group.group }"
            @click="currentGroup = group.group"
          >
            <b>{{ group.group }}</b>
            <small class="num">{{ group.items.length }} 项</small>
            <em v-if="groupDirty(group.group)" title="未保存"></em>
          </div>
        </aside>

        <!-- 当前分组表单 -->
        <section class="config-content card">
          <div class="config-head">
            <h2 class="card-title" style="margin: 0">{{ currentGroup }}</h2>
            <div class="spacer"></div>
            <t-button
              theme="primary"
              :loading="saving"
              :disabled="!currentItems.length"
              @click="onSave"
            >
              保存本组修改
            </t-button>
          </div>

          <div v-for="item in currentItems" :key="item.key" class="cfg-row">
            <div class="cfg-label">
              <div class="cfg-name">
                {{ keyMeta(item.key).label }}
                <span v-if="item.sensitive" class="dot-tag orange" style="margin-left: 6px">
                  <span class="dot"></span>敏感
                </span>
              </div>
              <div class="cfg-desc num">{{ item.key }}</div>
              <div class="cfg-desc">{{ keyMeta(item.key).description }}</div>
            </div>
            <div class="cfg-value">
              <!-- 敏感项：只展示脱敏形状，留空 = 保持不变 -->
              <template v-if="item.sensitive">
                <div class="secret-line">
                  <span class="masked-value num">{{ item.value || '（未配置）' }}</span>
                  <span class="secret-state" :class="{ empty: !item.value }">
                    {{ item.value ? '已配置' : '未配置' }}
                  </span>
                </div>
                <t-input
                  v-model="draft[item.key]"
                  type="password"
                  :placeholder="
                    item.value ? '已配置，输入以替换（留空保持不变）' : '未配置，请输入'
                  "
                  style="max-width: 360px"
                />
              </template>
              <!-- 普通项：原样展示当前值，留空 = 保持不变 -->
              <template v-else>
                <div class="secret-line">
                  <span class="masked-value num">{{ item.value || '（空）' }}</span>
                </div>
                <t-input
                  v-model="draft[item.key]"
                  :placeholder="'输入新值以更新（留空保持不变）'"
                  style="max-width: 420px"
                />
              </template>
              <div v-if="item.updated_at" class="cfg-desc num">
                最近更新：{{ formatTime(item.updated_at) }}
              </div>
            </div>
          </div>

          <div v-if="!currentItems.length" class="state-box">
            <b>该分组暂无配置项</b>
            <p>配置注册表未登记此分组的运行配置。</p>
          </div>

          <div class="save-hint muted">
            保存仅提交本组填写了新值的配置项；敏感项新值不在页面回显。
          </div>
        </section>
      </div>

      <div v-else-if="!loading" class="card state-box">
        <b>暂无配置项</b>
        <p>服务端配置注册表为空。</p>
      </div>
    </t-loading>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { getConfigs, updateConfigs } from '../features/admin/api.js';

// 配置键展示名（仅界面文案；键与生效逻辑以服务端注册表为准，未知键回退显示键名）
const KEY_META = {
  DIFY_API_URL: ['Dify API 地址', '全平台共用 Workflow 调用地址'],
  DIFY_USER: ['Dify Workflow User', '全平台共用调用用户'],
  DIFY_TIMEOUT_MS: ['Dify 调用超时（毫秒）', '全平台共用超时'],
  DIFY_WXPUSH_API_KEY: ['微信推送工作流 Key', '推送工作流独立 Key'],
  DIFY_WORKLOG_API_KEY: ['照片验证工作流 Key', '出工日志照片验证'],
  DIFY_SAFEDAY_API_KEY: ['安全日活动生成 Key', '安全日活动记录'],
  DIFY_QUIZ_API_KEY: ['安全测评工作流 Key', '安全测评'],
  WECHATPADPRO_TOKEN: ['Cloud Access Token', '加密保存，保存后重建连接'],
  WECHATPADPRO_API_BASE: ['API Base', 'WeChatPadPro 接口地址'],
  WECHATPADPRO_TICKET_PATH: ['换票接口路径', 'WebSocket 票据接口'],
  WECHATPADPRO_WS_URL: ['WebSocket 地址', '实时消息通道'],
  WECHATPADPRO_BOT_WXID: ['监听账号 wxid', '机器人账号'],
  WECHATPADPRO_FILE_APP_ID: ['文件下载 app_id', '文件/视频/语音下载'],
  LOG_RAW_MESSAGE: ['记录原始报文日志', '1=记录（脱敏，保留 7 天）'],
  CHATLOG_DIGEST_TIME: ['每日总结时间', '未消费聊天每日总结'],
  WEKNORA_API_URL: ['WeKnora API 地址', '知识库服务'],
  WEKNORA_API_KEY: ['WeKnora API Key', '知识库服务密钥'],
  WEKNORA_AGENT_ID: ['WeKnora Agent ID', '应答代理标识'],
  WEKNORA_SYNC_USER: ['WeKnora 同步用户名', '知识库同步账户'],
  WEKNORA_CHATLOG_KB_ID: ['聊天归档知识库 ID', '聊天日志归档'],
  WEKNORA_TREEHAZARD_KB_ID: ['树障知识库 ID', '树障隐患记录'],
  WEKNORA_CROSSING_KB_ID: ['交跨知识库 ID', '交跨记录'],
  CALLME_INGEST_TOKEN: ['Call Me 直推密钥', '会话捕获直推鉴权'],
  CHATLOG_INGEST_TOKEN: ['聊天归档直推密钥', '兼容直推对接'],
  TREEHAZARD_INGEST_TOKEN: ['树障直推密钥', '会话捕获直推鉴权'],
  CROSSING_INGEST_TOKEN: ['交跨直推密钥', '会话捕获直推鉴权'],
  COS_SECRET_ID: ['COS SecretId', '腾讯云对象存储'],
  COS_SECRET_KEY: ['COS SecretKey', '腾讯云对象存储'],
  COS_BUCKET: ['COS 存储桶', '对象存储桶名'],
  COS_REGION: ['COS 地域', '对象存储地域'],
  COS_WORKLOG_PREFIX: ['COS 照片前缀', '出工日志照片'],
  COS_WORKLOG_BASE_URL: ['COS 访问基址', '照片外链前缀'],
  COS_TREEHAZARD_PREFIX: ['COS 树障前缀', '树障照片'],
  AMAP_MAP_KEY: ['高德 Web 服务 Key', '出工与商旅地理服务'],
  AMAP_BASE_URL: ['高德接口 Base URL', '地理服务地址'],
  GLKVM_URL: ['GLKVM 远程地址', '远程连接计算机'],
  GLKVM_PASSWORD: ['GLKVM 密码', '远程连接计算机'],
  NETDISK_API_URL: ['OpenList API URL', '团队网盘中转'],
  NETDISK_USERNAME: ['网盘服务账户', 'OpenList 服务账户'],
  NETDISK_PASSWORD: ['网盘服务密码', 'OpenList 服务密码'],
  NETDISK_PUBLIC_ROOT: ['公共区根目录', '团队网盘公共区'],
  NETDISK_PERSONAL_ROOT: ['个人区根目录', '团队网盘个人区'],
  NETDISK_MAX_UPLOAD_MB: ['单文件上传上限（MB）', '上传限制'],
  NETDISK_THUMB_DIR: ['缩略图目录', '网盘缩略图缓存'],
  NETDISK_THUMB_MAX_MB: ['缩略图上限（MB）', '缩略图生成限制'],
  OO_PUBLIC_URL: ['ONLYOFFICE 浏览器地址', '在线编辑外网地址'],
  OO_INTERNAL_URL: ['ONLYOFFICE 内网地址', '容器互通地址'],
  OO_CALLBACK_BASE: ['ONLYOFFICE 回调基址', '编辑保存回调'],
  OO_JWT_SECRET: ['ONLYOFFICE JWT 密钥', '编辑器鉴权'],
  OO_FILE_TICKET_TTL: ['文件票据有效期（秒）', '在线编辑打开凭证'],
  OO_CALLBACK_TICKET_TTL: ['回调票据有效期（秒）', '编辑保存回调凭证'],
  SAFEDAY_DATA_DIR: ['记录与产物目录', '安全日活动文件'],
  SAFEDAY_DEFAULT_SUPERIOR: ['默认上级单位', '安全日活动记录'],
  SAFEDAY_CALLBACK_TOKEN: ['Dify 回调 Token', '活动生成回调鉴权'],
  SAFEDAY_SOFFICE_PATH: ['LibreOffice 路径', '附件转 PDF'],
  PREVIEW_VIEWER_URL: ['文档预览服务地址', 'Office 在线预览'],
  WORKLOG_DISPATCH_SYNC_TEAM: ['派车同步班组', '出工日志派车同步'],
  WORKLOG_DISPATCH_DEVICE_DDNS: ['派车同步设备 DDNS', '同步目标设备'],
  WORKLOG_DISPATCH_DEVICE_MAC: ['派车同步设备 MAC', '同步目标设备'],
  WORKLOG_DISPATCH_KVM_USER: ['派车同步 KVM 用户', '同步目标设备'],
  WORKLOG_DISPATCH_PREPARE_TIME: ['派车每日准备时间', '同步任务排程'],
  WORKLOG_DISPATCH_FETCH_TIME: ['派车每日取件时间', '同步任务排程'],
  TREEHAZARD_SYNC_TEAM: ['树障同步班组', '树障台账同步'],
  TREEHAZARD_SYNC_DEVICE_DDNS: ['树障同步设备 DDNS', '同步目标设备'],
  TREEHAZARD_SYNC_DEVICE_MAC: ['树障同步设备 MAC', '同步目标设备'],
  TREEHAZARD_SYNC_KVM_USER: ['树障同步 KVM 用户', '同步目标设备'],
  TREEHAZARD_SYNC_CMD_WINDOW_MIN: ['树障命令窗口（分钟）', '同步任务排程'],
  TREEHAZARD_SYNC_PREPARE_TIME: ['树障每日准备时间', '同步任务排程'],
  TREEHAZARD_SYNC_FETCH_TIME: ['树障每日取件时间', '同步任务排程'],
  CROSSING_SYNC_TEAM: ['交跨同步班组', '交跨台账同步'],
  CROSSING_SYNC_DEVICE_DDNS: ['交跨同步设备 DDNS', '同步目标设备'],
  CROSSING_SYNC_DEVICE_MAC: ['交跨同步设备 MAC', '同步目标设备'],
  CROSSING_SYNC_KVM_USER: ['交跨同步 KVM 用户', '同步目标设备'],
  CROSSING_SYNC_CMD_WINDOW_MIN: ['交跨命令窗口（分钟）', '同步任务排程'],
  CROSSING_SYNC_PREPARE_TIME: ['交跨每日准备时间', '同步任务排程'],
  CROSSING_SYNC_FETCH_TIME: ['交跨每日取件时间', '同步任务排程'],
  SGCC_JWT_SECRET: ['商旅 JWT 密钥', '商旅会话签发'],
  SGCC_SM2_SERVER_PUB: ['SM2 服务端公钥', '商旅协议'],
  SGCC_SM2_CLIENT_PRIV: ['SM2 客户端私钥', '商旅协议'],
  SGCC_RSA_PUB: ['RSA 公钥', '商旅协议'],
  SGCC_RSA_PRIV: ['RSA 私钥', '商旅响应解密'],
  SGCC_DCU_PUB: ['DCU 公钥', '商旅协议'],
  SGCC_WLA_PRIV: ['WLA 私钥', '商旅协议'],
  SGCC_AD_SECRET: ['广告池密钥', '商旅协议'],
  SGCC_DX_APPID: ['顶象 AppID', '商旅滑块验证'],
  SGCC_VERSION: ['商旅 App 版本', '版本伪装'],
  SGCC_VERSION_CODE: ['商旅构建号', '版本伪装'],
  SGCC_GRAY_VERSION: ['商旅灰度版本', '版本伪装'],
  SGCC_SYNC_TIME: ['商旅每日同步时间', '打卡任务排程'],
  SGCC_STARTCLOCK_REMIND_TIME: ['午间打卡提醒', '每日提醒'],
  SGCC_ENDCLOCK_REMIND_TIME: ['傍晚打卡提醒', '每日提醒'],
  SGCC_SYNC_INTERVAL_MS: ['同步间隔（毫秒）', '打卡同步节奏'],
  SGCC_PROXY_URL: ['SOCKS5 代理 URL', '出口代理（整项加密）'],
  SGCC_PROXY_PROBE_INTERVAL_MS: ['代理探测间隔（毫秒）', '出口代理健康检查'],
  JWT_EXPIRES: ['小程序 Token 有效期', '登录会话'],
  JWT_WEB_EXPIRES: ['网页 Token 有效期', '登录会话'],
  WX_APPID: ['小程序 AppID', '微信小程序登录'],
  WX_SECRET: ['小程序 AppSecret', '微信小程序登录'],
};

function keyMeta(key) {
  const meta = KEY_META[key];
  return meta ? { label: meta[0], description: meta[1] } : { label: key, description: '' };
}

// formatTime 把 RFC3339 时间格式化为可读本地样式。
function formatTime(value) {
  if (!value) return '';
  return String(value)
    .replace('T', ' ')
    .replace(/\.\d+.*$/, '')
    .replace(/Z$/, '');
}

const configGroups = ref([]);
const draft = reactive({}); // key → 输入中的新值（留空 = 保持不变，不提交）
const currentGroup = ref('');
const loading = ref(false);
const saving = ref(false);
const loadError = ref('');

const currentItems = computed(() => {
  const group = configGroups.value.find((g) => g.group === currentGroup.value);
  return group ? group.items : [];
});

// groupDirty 该分组是否存在填写未保存的新值
function groupDirty(groupName) {
  const group = configGroups.value.find((g) => g.group === groupName);
  if (!group) return false;
  return group.items.some((it) => String(draft[it.key] ?? '').trim() !== '');
}

async function load() {
  loading.value = true;
  loadError.value = '';
  try {
    configGroups.value = await getConfigs();
    if (!currentGroup.value && configGroups.value.length) {
      currentGroup.value = configGroups.value[0].group;
    }
    if (currentGroup.value && !configGroups.value.some((g) => g.group === currentGroup.value)) {
      currentGroup.value = configGroups.value.length ? configGroups.value[0].group : '';
    }
  } catch (err) {
    configGroups.value = [];
    loadError.value = err.message || '配置加载失败';
  } finally {
    loading.value = false;
  }
}

// 保存：仅收集本组填写了新值的项（留空跳过），真实请求服务端
async function onSave() {
  const items = [];
  for (const item of currentItems.value) {
    const v = String(draft[item.key] ?? '').trim();
    if (!v) continue;
    items.push({ key: item.key, value: v });
  }
  if (!items.length) {
    MessagePlugin.warning('没有填写任何新值（留空表示保持不变）');
    return;
  }
  saving.value = true;
  try {
    await updateConfigs(items);
    for (const it of items) {
      draft[it.key] = '';
    }
    MessagePlugin.success(`已保存 ${items.length} 项配置`);
    await load();
  } catch (err) {
    MessagePlugin.error(err.message || '配置保存失败');
  } finally {
    saving.value = false;
  }
}

onMounted(load);
</script>

<style scoped>
.note {
  padding: 10px 14px;
  color: #4e5969;
  font-size: 12px;
  line-height: 1.8;
  border-left: 3px solid #0e3da8;
  margin-bottom: 12px;
}
.config-layout {
  display: flex;
  gap: 12px;
  align-items: flex-start;
}
.config-side {
  width: 210px;
  flex: none;
  padding: 10px;
}
.side-label {
  color: #86909c;
  font-size: 11px;
  font-weight: 600;
  margin: 2px 6px 8px;
}
.config-nav {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border-radius: 8px;
  cursor: pointer;
  font-size: 13px;
}
.config-nav:hover {
  background: #f7f8fa;
}
.config-nav.on {
  background: #e8f0ff;
  color: #0e3da8;
}
.config-nav b {
  flex: 1;
  min-width: 0;
}
.config-nav small {
  color: #86909c;
  font-size: 11px;
}
.config-nav em {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #ff7d00;
}
.config-content {
  flex: 1;
  min-width: 0;
}
.config-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 6px;
}
.cfg-row {
  display: flex;
  gap: 16px;
  padding: 14px 0;
  border-bottom: 1px solid #e5e6eb;
}
.cfg-row:last-child {
  border-bottom: none;
}
.cfg-label {
  width: 260px;
  flex: none;
}
.cfg-name {
  font-size: 13px;
  font-weight: 600;
  color: #1d2129;
}
.cfg-desc {
  margin-top: 4px;
  color: #86909c;
  font-size: 11px;
  line-height: 1.7;
}
.cfg-value {
  flex: 1;
  min-width: 0;
}
.secret-line {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
}
.masked-value {
  color: #4e5969;
  font-size: 12px;
}
.secret-state {
  color: #0b8a37;
  font-size: 11px;
}
.secret-state.empty {
  color: #d25f00;
}
.save-hint {
  margin-top: 12px;
  font-size: 11px;
}
.state-box {
  padding: 32px 12px;
  text-align: center;
  color: #4e5969;
}
.state-box b {
  display: block;
  color: #1d2129;
  margin-bottom: 6px;
}
.state-box p {
  margin: 0;
  font-size: 12px;
  color: #86909c;
}
.state-box.error b {
  color: #f53f3f;
}
</style>
