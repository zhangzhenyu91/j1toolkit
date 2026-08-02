/* ============================================================
   出工日志 WorkLogs · 登录页
   - 已登录（GET /api/me 返回 ok）直接跳主应用
   - 提交 POST /api/login，成功跳 /，失败红字提示
   ============================================================ */
(function () {
  'use strict';

  var $ = function (s) { return document.querySelector(s); };
  var form = $('#loginForm');
  var btn = $('#loginBtn');
  var errBox = $('#errBox');

  /* 已登录则直接进入主应用（相对路径跳转，兼容反代前缀部署） */
  fetch('api/me', { credentials: 'same-origin' })
    .then(function (res) { return res.ok ? res.json() : null; })
    .then(function (j) {
      if (j && j.ok) window.location.replace('index.html');
    })
    .catch(function () { /* 网络异常时停留在登录页 */ });

  function showErr(msg) {
    errBox.textContent = msg || '';
  }

  form.addEventListener('submit', function (e) {
    e.preventDefault();
    if (btn.disabled) return; // 防连点

    var username = $('#username').value.trim();
    var password = $('#password').value;
    if (!username || !password) {
      showErr('请输入账号和密码');
      return;
    }

    showErr('');
    btn.disabled = true;
    btn.textContent = '登录中…';

    fetch('api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ username: username, password: password })
    })
      .then(function (res) { return res.json().catch(function () { return null; }); })
      .then(function (j) {
        if (j && j.ok) {
          window.location.replace('index.html');
          return;
        }
        showErr((j && j.error) || '登录失败，请稍后重试');
      })
      .catch(function () {
        showErr('网络异常，请检查连接后重试');
      })
      .then(function () {
        btn.disabled = false;
        btn.textContent = '登 录';
      });
  });
})();
