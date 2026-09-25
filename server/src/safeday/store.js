// 安全日活动记录：JSON 文件存储（自 SafeDayLogs 独立服务原样移植）
// 记录存 {SAFEDAY_DATA_DIR}/records.json，生成产物 docx 存 {SAFEDAY_DATA_DIR}/docs/
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const config = require('../config');

const DATA_DIR = config.safeday.dataDir;
const DATA_FILE = path.join(DATA_DIR, 'records.json');

function ensureFile() {
  if (!fs.existsSync(DATA_DIR)) {
    fs.mkdirSync(DATA_DIR, { recursive: true });
  }
  if (!fs.existsSync(DATA_FILE)) {
    fs.writeFileSync(DATA_FILE, '[]', 'utf8');
  }
}

function readAll() {
  ensureFile();
  try {
    const raw = fs.readFileSync(DATA_FILE, 'utf8');
    const arr = JSON.parse(raw);
    return Array.isArray(arr) ? arr : [];
  } catch (e) {
    return [];
  }
}

function writeAll(records) {
  ensureFile();
  // 原子写：先写临时文件再 rename，避免进程崩溃截断 records.json 致 readAll 静默回退 []
  const tmp = `${DATA_FILE}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(records, null, 2), 'utf8');
  fs.renameSync(tmp, DATA_FILE);
}

// 按 createdAt 倒序；teamName 传入时只列该班组
function list(teamName) {
  const all = readAll().sort((a, b) => (b.createdAt || '').localeCompare(a.createdAt || ''));
  if (!teamName) return all;
  return all.filter((r) => (r.team || '') === teamName);
}

// 同一（班组 + date）只保留最新一条：插入前删除同班组同 date 旧记录；
// 返回 { record, replaced }——replaced 为被覆盖的旧记录，供调用方在新生成失败时清理其孤儿产物
// （成功时同路径旧文件已被回调新渲染覆盖，无需处理；故不可在成功前提前删）
function create(record) {
  const replaced = [];
  const records = readAll().filter((r) => {
    const drop = r.date === record.date && (r.team || '') === (record.team || '');
    if (drop) replaced.push(r);
    return !drop;
  });
  const full = {
    id: `${Date.now()}-${crypto.randomBytes(4).toString('hex')}`,
    status: 'processing',
    createdAt: new Date().toISOString(),
    ...record,
  };
  records.push(full);
  writeAll(records);
  return { record: full, replaced };
}

function update(id, patch) {
  const records = readAll();
  const idx = records.findIndex((r) => r.id === id);
  if (idx === -1) return null;
  records[idx] = { ...records[idx], ...patch };
  if (patch.error === undefined) {
    delete records[idx].error;
  }
  writeAll(records);
  return records[idx];
}

function get(id) {
  return readAll().find((r) => r.id === id) || null;
}

function remove(id) {
  const records = readAll();
  const idx = records.findIndex((r) => r.id === id);
  if (idx === -1) return null;
  const [removed] = records.splice(idx, 1);
  writeAll(records);
  return removed;
}

// 班组迁移：无 team 字段的旧记录统一回填为指定班组名；返回是否有改动
function backfillTeam(defaultTeamName) {
  const records = readAll();
  let changed = false;
  for (const r of records) {
    if (!r.team) {
      r.team = defaultTeamName;
      changed = true;
    }
  }
  if (changed) writeAll(records);
  return changed;
}

// 班组改名级联：同步改写所有记录的 team 字段；返回改动条数
function renameTeam(oldName, newName) {
  const records = readAll();
  let count = 0;
  for (const r of records) {
    if (r.team === oldName) {
      r.team = newName;
      count++;
    }
  }
  if (count) writeAll(records);
  return count;
}

// ===== 生成表单默认值（按班组记忆：上级参加人员 / 记录人；存 form-defaults.json，键=班组名） =====
const DEFAULTS_FILE = path.join(DATA_DIR, 'form-defaults.json');

function readDefaults() {
  ensureFile();
  try {
    if (!fs.existsSync(DEFAULTS_FILE)) return {};
    const obj = JSON.parse(fs.readFileSync(DEFAULTS_FILE, 'utf8'));
    return obj && typeof obj === 'object' ? obj : {};
  } catch (e) {
    return {};
  }
}

// 取某班组的表单默认值（无记录返回空对象，调用方自行回落）
function getFormDefaults(teamName) {
  return readDefaults()[teamName] || {};
}

// 写入某班组的表单默认值（浅合并 patch；仅保留非空字符串值）
function saveFormDefaults(teamName, patch) {
  const all = readDefaults();
  const cur = all[teamName] || {};
  for (const [k, v] of Object.entries(patch || {})) {
    if (typeof v === 'string' && v.trim()) cur[k] = v.trim();
  }
  all[teamName] = cur;
  ensureFile();
  fs.writeFileSync(DEFAULTS_FILE, JSON.stringify(all, null, 2), 'utf8');
}

module.exports = { list, create, update, get, remove, backfillTeam, renameTeam, getFormDefaults, saveFormDefaults };
