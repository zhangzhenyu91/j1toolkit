// 网盘上次访问位置记忆（按用户隔离）：进入网盘恢复上次空间与目录，不重置回根目录。
// 校验口径同 Go netdisk.NormRel：反斜杠转正、压缩重复斜杠、拒 "."/".." 越界段与超长；
// 非法或不可用的记录一律忽略（回落默认位置），不落库不回传服务端。

const KEY_PREFIX = 'shade.netdisk.lastloc.';

// userKey 记忆键（按用户隔离：用户 id 优先，缺省回落用户名）。
function userKey(user) {
  const id = user && (user.id || user.username);
  return id ? KEY_PREFIX + id : '';
}

// normDir 记忆路径规范化：'' 或 '/' 表示空间根；非法（越界段/超长）返回 null。
export function normDir(input) {
  let p = String(input == null ? '' : input)
    .trim()
    .replace(/\\/g, '/');
  if (!p || p === '/') return '';
  if (!p.startsWith('/')) p = `/${p}`;
  const parts = p.split('/').filter(Boolean);
  if (parts.some((seg) => seg === '..' || seg === '.')) return null;
  const joined = parts.join('/');
  return joined.length > 200 ? null : joined;
}

// loadLastLoc 读取用户上次访问位置；非法记录返回 null。
export function loadLastLoc(user) {
  const key = userKey(user);
  if (!key) return null;
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return null;
    const rec = JSON.parse(raw);
    const space = rec && rec.space;
    if (space !== 'my' && space !== 'public') return null;
    const path = normDir(rec.path);
    if (path === null) return null;
    return { space, path };
  } catch (e) {
    return null;
  }
}

// saveLastLoc 记住用户当前空间与目录（非法路径按根目录记）。
export function saveLastLoc(user, space, path) {
  const key = userKey(user);
  if (!key) return;
  if (space !== 'my' && space !== 'public') return;
  const p = normDir(path);
  try {
    localStorage.setItem(key, JSON.stringify({ space, path: p === null ? '' : p }));
  } catch (e) {
    // localStorage 不可用时忽略
  }
}
