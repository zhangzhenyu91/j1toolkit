// 运行配置生效口径：绝大多数键热生效；JWT 有效期与日志保留策略只在启动时读取，保存后必须重启。
// JWT 仅影响重启后新签发的登录令牌（已登录会话不追溯）；日志保留由启动时的保留排程沿用。
export const RESTART_REQUIRED_KEYS = [
  'JWT_EXPIRES',
  'JWT_WEB_EXPIRES',
  'APP_LOG_RETENTION_DAYS',
  'APP_LOG_ARCHIVE_RETENTION_DAYS',
];

export const CONFIG_EFFECT_NOTICE =
  '大多数运行配置保存后即时生效；JWT 有效期与日志保留策略为例外：保存后需重启服务。JWT 仅对重启后新签发的登录令牌生效，已登录会话不受影响；日志保留天数由重启后的清理排程沿用。启动级配置仍由部署环境变量提供。';

export function requiresRestart(key) {
  return RESTART_REQUIRED_KEYS.includes(key);
}

export function configEffectHint(key) {
  if (!requiresRestart(key)) {
    return '保存后下一轮读取即时生效（无需重启）。';
  }
  if (key === 'APP_LOG_RETENTION_DAYS' || key === 'APP_LOG_ARCHIVE_RETENTION_DAYS') {
    return '保存后需重启服务才生效；当前清理排程沿用启动时读取的保留天数。';
  }
  return '保存后需重启服务才生效；且仅影响重启后新签发的登录令牌，已登录会话保持不变。';
}
