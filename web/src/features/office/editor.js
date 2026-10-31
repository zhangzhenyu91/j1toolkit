// ONLYOFFICE 承载页纯逻辑：安全销毁编辑器实例（实例不存在/销毁异常都不阻断离页）。

// destroyEditorSafely 销毁 OO 实例，成功返回 true；空实例或内部异常返回 false，不向外抛。
export function destroyEditorSafely(editor) {
  if (!editor || typeof editor.destroyEditor !== 'function') return false;
  try {
    editor.destroyEditor();
    return true;
  } catch (e) {
    return false;
  }
}
