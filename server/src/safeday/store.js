// 安全日活动记录：JSON 文件存储（自 SafeDayLogs 独立服务原样移植）
// 记录存 {SAFEDAY_DATA_DIR}/records.json，生成产物 docx 存 {SAFEDAY_DATA_DIR}/docs/
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const config = require('../config');

const DATA_DIR = path.resolve(config.safeday.dataDir);
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
  fs.writeFileSync(DATA_FILE, JSON.stringify(records, null, 2), 'utf8');
}

// 按 createdAt 倒序
function list() {
  return readAll().sort((a, b) => (b.createdAt || '').localeCompare(a.createdAt || ''));
}

// 同一 date 只保留最新一条：插入前删除同 date 旧记录
function create(record) {
  const records = readAll().filter((r) => r.date !== record.date);
  const full = {
    id: `${Date.now()}-${crypto.randomBytes(4).toString('hex')}`,
    status: 'processing',
    createdAt: new Date().toISOString(),
    ...record,
  };
  records.push(full);
  writeAll(records);
  return full;
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

module.exports = { list, create, update, get, remove };
