// KVM 远程管理子模块：设备列表实时代理自 GLKVM Cloud 平台（员工账号代登），
// 终端/远程控制经 /jump 签发带 sid 的跳转地址（平台 jump.html 种 Cookie），实现仅能通过壹匣登录
// 挂载：KVM_ENABLED=true 时由入口挂载（/api/v1/kvm）；GLKVM_* 配置见 .env.example
const express = require('express');
const config = require('../config');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { ok, fail } = require('../utils/resp');
const glkvm = require('./glkvm');

const router = express.Router();

// 本模块全部接口需登录 + kvm 应用权限
router.use(auth, requireApp('kvm'));

// 设备列表（透传 q/status/groupId 过滤；以当前员工账号代登平台，可见范围随其平台权限）
router.get('/devices', async (req, res) => {
  try {
    const items = await glkvm.listDevices(req.query, req.user.username);
    return ok(res, { items });
  } catch (err) {
    // glkvm.js 抛出的均为组装好的对用户可读文案
    return fail(res, 502, 50201, err && err.message ? err.message : 'GLKVM 平台访问失败');
  }
});

// 跳转目标白名单（与平台前端路由同构）：终端为 SPA hash 路由，远程控制为平台 /web/ 代理路由
const JUMP_TARGETS = {
  term: (ddns) => `/#/rtty/${ddns}`,
  desk: (ddns) => `/web/${ddns}/https/${encodeURIComponent('127.0.0.1:443/')}`,
};

// 签发带态跳转地址：以当前员工账号取平台会话 sid，拼平台 jump.html 地址（sid 走 hash，不经服务器日志）
router.post('/jump', async (req, res) => {
  const body = req.body || {};
  const build = JUMP_TARGETS[String(body.to || '')];
  const ddns = String(body.ddns || '');
  if (!build) return fail(res, 400, 40001, '参数错误：to');
  // ddns 仅允许字母数字与 . _ -（禁止路径字符，防拼接注入）
  if (!/^[A-Za-z0-9._-]{1,64}$/.test(ddns)) return fail(res, 400, 40001, '参数错误：ddns');
  try {
    const sid = await glkvm.getSessionToken(req.user.username);
    const url = `${config.kvm.url}/jump.html#sid=${encodeURIComponent(sid)}` +
      `&to=${encodeURIComponent(build(ddns))}`;
    return ok(res, { url });
  } catch (err) {
    return fail(res, 502, 50201, err && err.message ? err.message : 'GLKVM 平台访问失败');
  }
});

module.exports = router;
