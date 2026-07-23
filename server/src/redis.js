// Redis：JWT 黑名单（退出登录即时失效）
// Redis 异常时降级为仅记录日志、不阻断业务（仅影响退出登录的即时失效语义）
const Redis = require('ioredis');
const config = require('./config');

const client = new Redis({
  host: config.redis.host,
  port: config.redis.port,
  password: config.redis.password || undefined,
  retryStrategy: (times) => Math.min(times * 1000, 10000),
});

client.on('connect', () => console.log('[Redis] 已连接'));
client.on('error', (err) => console.error('[Redis] 连接异常：', err.message));

const keyOf = (token) => `jwt:blacklist:${token}`;

async function blacklistToken(token, ttlSec) {
  try {
    await client.set(keyOf(token), '1', 'EX', Math.max(ttlSec, 1));
  } catch (err) {
    console.error('[Redis] 黑名单写入失败：', err.message);
  }
}

async function isBlacklisted(token) {
  try {
    return (await client.get(keyOf(token))) === '1';
  } catch (err) {
    return false;
  }
}

module.exports = { blacklistToken, isBlacklisted };
