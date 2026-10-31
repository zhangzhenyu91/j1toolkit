// 粘贴文本自动解析调度：防抖、请求序号（丢弃过期响应）与解析态收敛。
// 纯 JS、可注入定时器与请求函数，Node 测试与 Vue 页面共用。
export function createPreviewScheduler({
  request,
  delay = 400,
  setTimeout: setTimeoutFn = setTimeout,
  clearTimeout: clearTimeoutFn = clearTimeout,
  onPreview = () => {},
  onEmpty = () => {},
  onError = () => {},
  onState = () => {},
} = {}) {
  let token = 0;
  let timer = null;
  let textValue = '';
  let state = { parsing: false, pending: false, source: '' };

  function emit(patch) {
    state = { ...state, ...patch };
    onState(state);
  }

  function clearTimer() {
    if (timer !== null) {
      clearTimeoutFn(timer);
      timer = null;
    }
  }

  function settle(myToken, patch) {
    if (myToken !== token) return false;
    emit({ parsing: false, pending: false, ...patch });
    return true;
  }

  async function run(myToken, params, meta) {
    clearTimer();
    emit({ parsing: true, pending: false, source: meta.source });
    try {
      const result = await request(params);
      if (myToken !== token) return;
      const codes = Array.isArray(result?.codes) ? result.codes : [];
      if (codes.length === 0) onEmpty(meta);
      else onPreview(result, meta);
      settle(myToken);
    } catch (err) {
      if (myToken !== token) return;
      onError(err, meta);
      settle(myToken);
    }
  }

  return {
    get state() {
      return { ...state };
    },
    setText(text) {
      const value = String(text == null ? '' : text).trim();
      textValue = value;
      token += 1;
      clearTimer();
      if (!value) {
        emit({ parsing: false, pending: false, source: '' });
        return false;
      }
      const myToken = token;
      emit({ parsing: true, pending: true, source: 'text' });
      timer = setTimeoutFn(() => {
        timer = null;
        if (myToken !== token) return;
        run(myToken, { codes_text: value }, { source: 'text' });
      }, delay);
      return true;
    },
    parseNow() {
      const value = String(textValue).trim();
      if (!value) return false;
      token += 1;
      clearTimer();
      run(token, { codes_text: value }, { source: 'text' });
      return true;
    },
    previewFile(params, meta = { source: 'file' }) {
      token += 1;
      clearTimer();
      run(token, params, meta);
      return true;
    },
    cancel() {
      token += 1;
      clearTimer();
      emit({ parsing: false, pending: false, source: '' });
    },
    dispose() {
      clearTimer();
    },
  };
}
