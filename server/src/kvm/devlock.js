// KVM 设备维护锁：锁定期间「远程连接计算机 / 文件传输」对该设备暂不可用（每日派车单同步窗口用，
// 见 worklog/dispatch-sync.js）；锁存 Redis（key: kvm:maint:{ddns}，value=原因文案），TTL 兜底自动解禁；
// Redis 异常时降级为日志、按未锁放行（fail-open，与 redis.js 黑名单同口径）
const { client } = require('../redis');

const keyOf = (ddns) => `kvm:maint:${ddns}`;

async function lock(ddns, reason, ttlSec = 7200) {
  try {
    await client.set(keyOf(ddns), String(reason || '设备维护中'), 'EX', Math.max(ttlSec, 60));
  } catch (err) {
    console.error('[设备维护锁] 写入失败：', err.message);
  }
}

async function unlock(ddns) {
  try {
    await client.del(keyOf(ddns));
  } catch (err) {
    console.error('[设备维护锁] 解除失败：', err.message);
  }
}

// 命中返回原因文案（锁定中），未锁/异常返回 null
async function getReason(ddns) {
  try {
    return await client.get(keyOf(ddns));
  } catch (err) {
    console.error('[设备维护锁] 读取失败：', err.message);
    return null;
  }
}

module.exports = { lock, unlock, getReason };
