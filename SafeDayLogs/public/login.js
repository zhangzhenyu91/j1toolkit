(function () {
  'use strict';

  var form = document.getElementById('loginForm');
  var usernameInput = document.getElementById('usernameInput');
  var passwordInput = document.getElementById('passwordInput');
  var errorEl = document.getElementById('loginError');
  var loginBtn = document.getElementById('loginBtn');
  var submitting = false;

  function showError(msg) {
    errorEl.textContent = msg || '';
    errorEl.classList.toggle('show', !!msg);
  }

  /* 已登录则直接进入主页面 */
  fetch('api/me', { headers: { 'Accept': 'application/json' } })
    .then(function (res) {
      return res.json().catch(function () { return null; });
    })
    .then(function (data) {
      if (data && data.ok) { location.href = './'; }
    })
    .catch(function () { /* 忽略异常，停留在登录页 */ });

  form.addEventListener('submit', function (e) {
    e.preventDefault();
    if (submitting) { return; }
    var username = usernameInput.value.trim();
    var password = passwordInput.value;
    if (!username || !password) {
      showError('请输入账号和密码');
      return;
    }
    showError('');
    submitting = true;
    loginBtn.disabled = true;
    loginBtn.textContent = '正在登录…';

    fetch('api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
      body: JSON.stringify({ username: username, password: password }),
    })
      .then(function (res) {
        return res.json().catch(function () {
          throw new Error('服务器响应异常（' + res.status + '）');
        }).then(function (data) {
          if (!res.ok || !data || data.ok === false) {
            throw new Error((data && data.error) || ('登录失败（' + res.status + '）'));
          }
          return data;
        });
      })
      .then(function () {
        location.href = './';
      })
      .catch(function (err) {
        showError(err.message || '登录失败，请稍后重试');
      })
      .then(function () {
        submitting = false;
        loginBtn.disabled = false;
        loginBtn.textContent = '登 录';
      });
  });
})();
