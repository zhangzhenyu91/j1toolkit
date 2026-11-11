<template>
  <div class="dev-script-page">
    <div class="page-split">
      <div class="page-split-col">
        <t-card>
          <div class="sec-title">运行脚本</div>
          <div class="row">
            <t-input v-model="path" class="grow" placeholder="选择 .py 脚本，或输入路径" />
            <t-button variant="outline" @click="pick">选择文件</t-button>
          </div>
          <div class="row" style="margin-top: 10px">
            <span class="muted">参数</span>
            <t-input v-model="args" class="grow" placeholder="传给脚本的参数（可留空）" />
          </div>
          <div class="row" style="margin-top: 10px">
            <span class="muted">工作目录</span>
            <t-input v-model="cwd" class="grow" placeholder="默认脚本所在目录" />
            <t-button variant="outline" @click="pickDir">选择目录</t-button>
          </div>
          <div class="row" style="margin-top: 12px">
            <t-button theme="primary" :disabled="running || !allowScripts" @click="start"
              >运行</t-button
            >
            <t-button v-if="running" theme="danger" variant="outline" @click="cancel"
              >停止</t-button
            >
            <span class="muted">使用客户端同款 Python 运行时；脚本以当前用户权限运行。</span>
          </div>
          <t-alert v-if="!allowScripts" theme="warning" style="margin-top: 10px">
            脚本测试已在设置页关闭（dev.allow_scripts），当前不能运行脚本，需要时请到设置页重新开启。
          </t-alert>
          <div style="margin-top: 10px">
            <t-progress
              theme="plump"
              :percentage="progress.pct"
              :label="progress.text"
              color="#0E3DA8"
              track-color="rgba(14,61,168,.10)"
            />
          </div>
        </t-card>

        <t-card>
          <div class="sec-title">运行环境</div>
          <div class="kv">
            <span class="k">解释器</span><span class="break">{{ env.python || '-' }}</span>
            <span class="k">版本</span><span>{{ env.version || '-' }}</span>
            <span class="k">运行方式</span
            ><span>{{ env.frozen ? '打包核心（uvmp-core）' : '源码 Python' }}</span>
            <span class="k">配置文件</span><span class="break">{{ env.config_path || '-' }}</span>
            <span class="k">状态目录</span><span class="break">{{ env.state_dir || '-' }}</span>
          </div>
          <p class="muted" style="margin: 8px 0 0">
            脚本默认在自身目录运行；需要客户端配置/状态时直接读
            <code>UVMP_TOOLKIT_CONFIG</code>，无需另配环境。
          </p>
          <p class="muted" style="margin: 6px 0 0">
            注意：脚本以当前用户权限运行，可读取本机配置文件与其中保存的账号凭据；只运行可信脚本。
          </p>
        </t-card>
      </div>

      <div class="page-split-col">
        <TaskResult :outcome="outcome" :logs="logs" />
      </div>
    </div>
  </div>
</template>

<script setup>
import { onActivated, reactive, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { call, pickDirectory, pickFile, runJob } from '../api';
import TaskResult from '../components/TaskResult.vue';

const path = ref('');
const args = ref('');
const cwd = ref('');
const running = ref(false);
// 开发者工具开关：默认允许，仅在读到的配置明确为 false 时禁用运行（与核心判定一致）
const allowScripts = ref(true);
const logs = ref([]);
const progress = reactive({ pct: 0, text: '' });
const outcome = ref({ status: '尚未开始', summary: '', failed: false });
const env = reactive({
  python: '',
  version: '',
  frozen: false,
  config_path: '',
  state_dir: '',
});

let currentJob = null;
let envLoaded = false;

async function loadAllowScripts() {
  // 每次激活都重读：在设置页关掉开关后切回本页要立刻反映，不能只认首次加载
  try {
    const cfg = await call('getConfig');
    allowScripts.value = (cfg.dev || {}).allow_scripts !== false;
  } catch (_e) {
    /* 配置读取失败按默认允许处理，最终仍由核心再判一次 */
  }
}

async function loadEnv() {
  if (envLoaded) return;
  try {
    const data = await call('getDevScriptEnv');
    Object.assign(env, data || {});
    envLoaded = true;
  } catch (_e) {
    /* 环境信息读取失败不阻断脚本运行 */
  }
}

async function pick() {
  const p = await pickFile([{ name: 'Python 脚本', extensions: ['py'] }]);
  if (p) path.value = p;
}

async function pickDir() {
  const p = await pickDirectory(cwd.value || undefined);
  if (p) cwd.value = p;
}

function pushLog(line) {
  logs.value.push(line);
  if (logs.value.length > 2000) logs.value.splice(0, logs.value.length - 2000);
}

function start() {
  if (running.value) return;
  if (!allowScripts.value) {
    MessagePlugin.warning('脚本测试已在设置页关闭（dev.allow_scripts），请先开启后再运行');
    return;
  }
  if (!path.value.trim()) {
    MessagePlugin.warning('请选择要运行的 .py 脚本');
    return;
  }
  logs.value = [];
  progress.pct = 0;
  progress.text = '启动中';
  outcome.value = { status: '运行中', summary: '', failed: false };
  running.value = true;
  currentJob = runJob(
    'dev_script',
    { path: path.value.trim(), args: args.value.trim(), cwd: cwd.value.trim() },
    {
      onLog: pushLog,
      onProgress: (p) => {
        progress.pct = p.total ? Math.round((p.current / p.total) * 100) : 0;
        progress.text = p.label || '';
      },
      onDone: (job) => {
        running.value = false;
        progress.pct = 100;
        progress.text = '已结束';
        const ok = job && job.status === 'success';
        outcome.value = {
          status: ok ? '成功' : '失败',
          summary: ok
            ? `退出码 0${job.result && job.result.duration_ms ? '　' + job.result.duration_ms + ' ms' : ''}`
            : (job && job.error) || '脚本执行失败',
          failed: !ok,
        };
      },
      onError: (err) => {
        running.value = false;
        progress.text = '启动失败';
        outcome.value = { status: '启动失败', summary: String(err), failed: true };
      },
    }
  );
}

function cancel() {
  if (currentJob) currentJob.cancel();
}

onActivated(() => {
  loadEnv();
  loadAllowScripts();
});
</script>

<style scoped>
.break {
  word-break: break-all;
}
</style>
