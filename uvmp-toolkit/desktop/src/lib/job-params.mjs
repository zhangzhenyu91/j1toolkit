// 历史参数快照：仅保存可重试任务的非敏感参数（localStorage），供执行历史「参数回填」。
const PREFIX = 'shell.job_params.';
const SUFFIX = '.v1';
const INDEX_KEY = 'shell.job_params.index.v1';
const MAX_BYTES = 512 * 1024;
const ALLOWED = new Set([
  'pcd_export',
  'daily_sync',
  'tree_sync',
  'crossing_sync',
  'course_autoplay',
]);
const SENSITIVE = [
  'password',
  'passwd',
  'secret',
  'token',
  'credential',
  'authorization',
  'cookie',
  'sso',
];

function keyFor(jobID) {
  return PREFIX + jobID + SUFFIX;
}

function readIndex(storage) {
  try {
    const parsed = JSON.parse(storage.getItem(INDEX_KEY) || '[]');
    return Array.isArray(parsed) ? parsed.filter((entry) => typeof entry === 'string') : [];
  } catch {
    return [];
  }
}

function writeIndex(storage, entries) {
  storage.setItem(INDEX_KEY, JSON.stringify(entries));
}

function hasSensitiveKey(value) {
  if (Array.isArray(value)) return value.some(hasSensitiveKey);
  if (!value || typeof value !== 'object') return false;
  return Object.entries(value).some(([key, nested]) => {
    const normalized = key.toLowerCase();
    return SENSITIVE.some((needle) => normalized.includes(needle)) || hasSensitiveKey(nested);
  });
}

export function saveJobParams(storage, jobID, appID, params) {
  if (!jobID || !ALLOWED.has(appID)) return false;
  let snapshot;
  try {
    snapshot = params == null ? {} : JSON.parse(JSON.stringify(params));
  } catch {
    return false;
  }
  if (!snapshot || typeof snapshot !== 'object' || Array.isArray(snapshot)) return false;
  if (hasSensitiveKey(snapshot)) return false;
  const encoded = JSON.stringify({ app_id: appID, params: snapshot });
  if (encoded.length > MAX_BYTES) return false;
  try {
    storage.setItem(keyFor(jobID), encoded);
  } catch {
    return false;
  }
  const entry = `${appID}:${jobID}`;
  const index = readIndex(storage);
  if (!index.includes(entry)) {
    index.push(entry);
    writeIndex(storage, index);
  }
  return true;
}

export function readJobParams(storage, jobID) {
  if (!jobID) return null;
  try {
    const parsed = JSON.parse(storage.getItem(keyFor(jobID)) || 'null');
    if (!parsed || !ALLOWED.has(parsed.app_id)) return null;
    if (!parsed.params || typeof parsed.params !== 'object' || Array.isArray(parsed.params)) {
      return null;
    }
    if (hasSensitiveKey(parsed.params)) return null;
    return {
      appID: parsed.app_id,
      params: JSON.parse(JSON.stringify(parsed.params)),
    };
  } catch {
    return null;
  }
}

export function hasJobParams(storage, jobID) {
  return readJobParams(storage, jobID) !== null;
}

export function deleteJobParams(storage, jobID) {
  if (!jobID) return;
  storage.removeItem(keyFor(jobID));
  writeIndex(
    storage,
    readIndex(storage).filter((entry) => entry.split(':').slice(1).join(':') !== jobID)
  );
}

export function deleteAppJobParams(storage, appID) {
  if (!appID) return;
  const remaining = [];
  for (const entry of readIndex(storage)) {
    const [storedApp, ...rest] = entry.split(':');
    const jobID = rest.join(':');
    if (storedApp === appID) {
      storage.removeItem(keyFor(jobID));
    } else {
      remaining.push(entry);
    }
  }
  writeIndex(storage, remaining);
}

export { MAX_BYTES };
