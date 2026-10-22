// 夜间任务串行队列：各子应用按「北京时间 HH:mm」登记夜间任务（如内网 U 盘同步、商旅每日核查），
// 同一时刻的任务编为一组共享一个每日自排程定时器，到点按登记序串行 await 执行；
// 单任务失败 catch 后 console.error 并继续下一个，不互相拖垮；排程口径同 dispatch-sync（setTimeout 自排程 + unref）
const { nextDailyRunUtc } = require('./cndate');

const groups = new Map(); // 归一时刻 'HH:mm' -> { hh, mm, jobs: [{ name, job }], timer }

// 归一时刻文本：'23:0' → '23:00'；非法输入回退 23:00（hh 可能为 0（午夜），不能用 || 判缺省）
function normalizeTime(timeStr) {
  const [hh, mm] = String(timeStr || '')
    .split(':')
    .map((s) => parseInt(s, 10));
  const h = Number.isInteger(hh) && hh >= 0 && hh <= 23 ? hh : 23;
  const m = Number.isInteger(mm) && mm >= 0 && mm <= 59 ? mm : 0;
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`;
}

// 串行执行一组任务（按登记序；单任务失败不中断后续任务）
async function runJobs(timeStr, jobs) {
  for (const { name, job } of jobs) {
    try {
      await job();
    } catch (err) {
      console.error(`[夜间任务] ${timeStr} ${name} 执行失败：`, err);
    }
  }
}

// 组内排程：每天 timeStr（北京时间）执行本组任务并自排程
function scheduleGroup(timeStr, group) {
  const { nextUtc, now } = nextDailyRunUtc(group.hh, group.mm);
  group.timer = setTimeout(async () => {
    await runJobs(timeStr, group.jobs); // runJobs 内部已逐任务兜底
    scheduleGroup(timeStr, group); // 排次日
  }, nextUtc - now);
  group.timer.unref(); // 不阻塞进程退出
  console.log(
    `[夜间任务] ${timeStr} 组已排程（${group.jobs.map((j) => j.name).join('、')}）：${new Date(nextUtc).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' })}（北京时间）`
  );
}

// 登记夜间任务：name 供日志识别；timeStr 为北京时间 'HH:mm'；job 为 async 函数。
// 同一 timeStr 的任务到点按登记序串行执行；登记任意时刻即生效
function register(name, timeStr, job) {
  const key = normalizeTime(timeStr);
  let group = groups.get(key);
  if (!group) {
    const [hh, mm] = key.split(':').map((s) => parseInt(s, 10));
    group = { hh, mm, jobs: [], timer: null };
    groups.set(key, group);
  }
  group.jobs.push({ name, job });
  if (!group.timer) scheduleGroup(key, group); // 新组立即排程（首任务登记后再排，日志能列出任务名）
}

// 串行执行全部已登记任务（跨组按时刻序；手动触发/测试也可直接调）
async function runAll() {
  for (const [timeStr, group] of [...groups.entries()].sort()) {
    await runJobs(timeStr, group.jobs);
  }
}

module.exports = { register, runAll };
