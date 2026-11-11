// 题库搜题：题库本地存储（纯 Node，无 Electron 依赖，可 node 直测）。
// 存储布局（dir = userData/quiz-search）：
//   banks-index.json      {banks:[{id,name,sourceFile,importedAt,count,enabled}]}
//   banks/<id>.json       {id,name,questions:[{type,content,options[],answer,analysis}]}
// 字段口径对齐壹匣刷题（server/src/quiz/schema.js quiz_question）：
//   type: single 单选 / multiple 多选 / judge 判断；判断题固定选项 ["正确","错误"]，A=正确 B=错误。
//
// 可靠性约定（都是踩过的坑）：
//   - 索引与题库文件一律「写临时文件再 rename」原子落盘，断电/崩溃不留半截 JSON；
//   - 索引解析失败不静默清空：损坏文件改名留档 + 提示，再从 banks/ 重建；
//   - 索引与 banks/ 双向对账：孤儿题库文件自动补回，指向缺失文件的条目剔除；
//   - 所有对外 ID 走白名单校验，杜绝 ../ 之类的路径穿越。
'use strict';

const fs = require('fs');
const path = require('path');
const { normalize } = require('./match.cjs');

// Excel 表头别名 → 内部字段（兼容嗖嗖搜题表头与刷题导出表头）
const HEADER_ALIASES = {
  content: ['题干', '题目', 'question', 'content', 'stem'],
  answer: ['答案', 'answer'],
  analysis: ['解析', '答案解析', 'analysis'],
  type: ['题型', 'type'],
};
const OPTION_LETTERS = ['A', 'B', 'C', 'D', 'E', 'F'];
const ID_RE = /^[0-9a-z]{4,64}$/; // newId 产物：时间戳 base36 + 随机 4 位
const NAME_MAX = 80;

function newId() {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 6);
}

// 题库 ID 白名单：只允许小写字母数字，长度 4~64——同时挡住路径穿越与超长文件名
function assertId(id) {
  if (typeof id !== 'string' || !ID_RE.test(id)) throw new Error('题库 ID 非法：' + String(id));
  return id;
}

// 表头名归一化（去空白/全角转半角/小写）后查别名表
function mapHeaders(headerRow) {
  const colOf = {};
  headerRow.forEach((h, idx) => {
    const key = String(h == null ? '' : h)
      .trim()
      .replace(/[Ａ-Ｚａ-ｚ０-９]/g, (c) => String.fromCharCode(c.charCodeAt(0) - 0xfee0))
      .toLowerCase();
    if (!key) return;
    for (const [field, aliases] of Object.entries(HEADER_ALIASES)) {
      if (aliases.includes(key)) {
        colOf[field] = idx;
        return;
      }
    }
    const m = key.match(/^选项\s*([a-f])$/);
    if (m) {
      colOf['option' + m[1].toUpperCase()] = idx;
      return;
    }
  });
  return colOf;
}

function normAnswer(raw) {
  return String(raw == null ? '' : raw)
    .trim()
    .toUpperCase()
    .replace(/[，、\s]/g, '')
    .split('')
    .sort()
    .join('');
}

// 判断题答案归一：正确/对/√/T → A；错误/错/×/F → B
function normJudgeAnswer(a) {
  if (/^(正确|对|√|T|TRUE|YES)$/.test(a)) return 'A';
  if (/^(错误|错|×|X|F|FALSE|NO)$/.test(a)) return 'B';
  return a;
}

class BankStore {
  constructor(dir) {
    this.dir = dir;
    this.banksDir = path.join(dir, 'banks');
    this.indexPath = path.join(dir, 'banks-index.json');
    this.notice = ''; // 最近一次自愈提示（页面「技术诊断」展示）
    this._indexCache = null; // 索引对账结果缓存（见 _readIndex）
    this._indexStamp = '';
    fs.mkdirSync(this.banksDir, { recursive: true });
  }

  _bankPath(id) {
    return path.join(this.banksDir, assertId(id) + '.json');
  }

  // 原子写：临时文件同目录 → rename，避免半截文件；临时名带 pid 防并发互踩
  _writeJsonAtomic(file, obj) {
    const tmp = file + '.' + process.pid + '-' + Date.now() + '.tmp';
    fs.writeFileSync(tmp, JSON.stringify(obj, null, 2), 'utf8');
    try {
      fs.renameSync(tmp, file);
    } catch (e) {
      try {
        fs.unlinkSync(tmp);
      } catch (_e) {
        /* 临时文件已不在 */
      }
      throw e;
    }
  }

  // 索引写入统一入口：落盘后失效缓存戳（下一次 _readIndex 会重读）
  _writeIndex(idx) {
    this._writeJsonAtomic(this.indexPath, idx);
    this._indexCache = idx;
    try {
      const st = fs.statSync(this.indexPath);
      this._indexStamp = st.mtimeMs + ':' + st.size;
    } catch (_e) {
      this._indexStamp = '';
    }
  }

  _readBankRaw(id) {
    try {
      return fs.readFileSync(this._bankPath(id), 'utf8');
    } catch (_e) {
      return null;
    }
  }

  _readBank(id) {
    const raw = this._readBankRaw(id);
    if (raw === null) return null;
    try {
      const bank = JSON.parse(raw);
      if (!bank || typeof bank !== 'object' || !Array.isArray(bank.questions)) return null;
      return bank;
    } catch (_e) {
      return null;
    }
  }

  // 损坏索引留档（不删除用户数据），返回备份路径
  _backupIndex() {
    const stamp = new Date().toISOString().replace(/[:.]/g, '-');
    const backup = path.join(this.dir, 'banks-index.corrupt-' + stamp + '.json');
    try {
      fs.renameSync(this.indexPath, backup);
      return backup;
    } catch (_e) {
      return '';
    }
  }

  _bankFiles() {
    let names = [];
    try {
      names = fs.readdirSync(this.banksDir);
    } catch (_e) {
      return [];
    }
    return names
      .filter((f) => f.endsWith('.json'))
      .map((f) => f.slice(0, -5))
      .filter((id) => ID_RE.test(id));
  }

  _metaOf(id, bank) {
    return {
      id,
      name: String(bank.name || '题库'),
      sourceFile: String(bank.sourceFile || ''),
      importedAt: String(bank.importedAt || ''),
      count: bank.questions.length,
      enabled: bank.enabled === false ? false : true,
    };
  }

  // 读索引 + 与 banks/ 对账；索引损坏或需要自愈时写回。
  // 结果按索引文件 stat 缓存：页面每 2 秒轮询一次 state()，不能每次都把全部题库文件重读一遍。
  _readIndex() {
    let stamp = '';
    try {
      const st = fs.statSync(this.indexPath);
      stamp = st.mtimeMs + ':' + st.size;
    } catch (_e) {
      stamp = ''; // 无索引文件
    }
    if (this._indexCache && stamp === this._indexStamp) return this._indexCache;

    let raw = null;
    try {
      raw = fs.readFileSync(this.indexPath, 'utf8');
    } catch (_e) {
      raw = null; // 首次运行
    }
    let parsed = null;
    let dirty = false;
    if (raw !== null) {
      try {
        parsed = JSON.parse(raw);
      } catch (_e) {
        const backup = this._backupIndex();
        this.notice =
          '题库索引损坏已重建' + (backup ? '（原文件留档：' + path.basename(backup) + '）' : '');
        dirty = true;
      }
    }
    const banks = parsed && Array.isArray(parsed.banks) ? parsed.banks : [];
    if (parsed && !Array.isArray(parsed.banks)) dirty = true;

    const fileIds = new Set(this._bankFiles());
    const out = [];
    const kept = new Set();
    for (const meta of banks) {
      if (!meta || typeof meta !== 'object' || !ID_RE.test(String(meta.id))) {
        dirty = true;
        continue;
      }
      const id = String(meta.id);
      if (kept.has(id)) {
        dirty = true; // 重复条目（历史 bug 曾写出两份）：合并成一条
        continue;
      }
      if (!fileIds.has(id)) {
        dirty = true; // 指向缺失/非法文件：剔除（文件缺失无法恢复题目）
        continue;
      }
      const bank = this._readBank(id);
      if (!bank) {
        dirty = true; // 题库文件损坏：保留文件、剔除条目，提示用户重新导入
        this.notice = '题库文件损坏已跳过：banks/' + id + '.json（文件保留，可重新导入）';
        continue;
      }
      kept.add(id);
      const fresh = this._metaOf(id, bank);
      // 以题库文件为准校正 count/name（索引曾因旧版本写脏）
      const merged = Object.assign({}, fresh, {
        enabled: meta.enabled === false ? false : true,
      });
      if (
        meta.count !== merged.count ||
        meta.name !== merged.name ||
        meta.enabled !== merged.enabled
      ) {
        dirty = true;
      }
      out.push(merged);
    }
    // 孤儿题库文件：索引缺失但文件在，自动补回
    for (const id of fileIds) {
      if (kept.has(id)) continue;
      const bank = this._readBank(id);
      if (!bank) {
        this.notice = '题库文件损坏已跳过：banks/' + id + '.json（文件保留，可重新导入）';
        continue;
      }
      out.push(this._metaOf(id, bank));
      dirty = true;
    }
    const idx = { banks: out };
    if (dirty) {
      this._writeIndex(idx); // 内部会填缓存
    } else {
      this._indexCache = idx;
      this._indexStamp = stamp;
    }
    return idx;
  }

  list() {
    return this._readIndex().banks;
  }

  getBank(id) {
    if (typeof id !== 'string' || !ID_RE.test(id)) return null;
    return this._readBank(id);
  }

  /**
   * 导入题库。rows 为二维数组（首行表头；由 read-excel-file 或内置 xls 读取器产出）。
   * 返回 {id, name, count, dropped, enabled}
   */
  importRows(name, sourceFile, rows) {
    if (!Array.isArray(rows) || rows.length < 2) throw new Error('表格为空或缺少数据行');
    const colOf = mapHeaders(rows[0]);
    if (colOf.content === undefined || colOf.answer === undefined) {
      throw new Error('未识别到「题干」「答案」表头列');
    }
    const questions = [];
    const seen = new Set();
    let dropped = 0;
    for (let i = 1; i < rows.length; i++) {
      const r = rows[i];
      if (!Array.isArray(r)) continue;
      const content = String(r[colOf.content] == null ? '' : r[colOf.content]).trim();
      let answer = normAnswer(r[colOf.answer]);
      if (!content || !answer) {
        dropped++;
        continue;
      }
      const options = [];
      for (const L of OPTION_LETTERS) {
        const c = colOf['option' + L];
        const v = c === undefined ? '' : String(r[c] == null ? '' : r[c]).trim();
        if (v) options.push(v);
      }
      const analysis =
        colOf.analysis === undefined
          ? ''
          : String(r[colOf.analysis] == null ? '' : r[colOf.analysis]).trim();
      let type =
        colOf.type === undefined
          ? ''
          : String(r[colOf.type] == null ? '' : r[colOf.type])
              .trim()
              .toLowerCase();
      const typeMap = {
        单选: 'single',
        多选: 'multiple',
        判断: 'judge',
        single: 'single',
        multiple: 'multiple',
        judge: 'judge',
      };
      type = typeMap[type] || '';
      if (!type) {
        if (/^(正确|错误|对|错|√|×|T|F|TRUE|FALSE|YES|NO)$/.test(answer)) {
          type = 'judge';
        } else if (options.length === 0 && (answer === 'A' || answer === 'B')) {
          type = 'judge'; // 无选项且答案仅 A/B：判断题（表内省略了 正确/错误 选项列）
        } else if (answer.length > 1) {
          type = 'multiple';
        } else {
          type = 'single';
        }
      }
      if (type === 'judge') {
        answer = normJudgeAnswer(answer);
        if (options.length === 0) options.push('正确', '错误');
      }
      const key = normalize(content) + '|' + answer;
      if (seen.has(key)) {
        dropped++;
        continue;
      } // 库内去重（原软件 Distinct 语义）
      seen.add(key);
      questions.push({ type, content, options, answer, analysis, sort: questions.length });
    }
    if (questions.length === 0) throw new Error('未解析到有效题目，请检查表头与数据');
    const id = newId();
    const bankName = (
      String(name || '').trim() ||
      path.basename(String(sourceFile || '题库'), path.extname(String(sourceFile || '')))
    ).slice(0, NAME_MAX);
    const bank = {
      id,
      name: bankName,
      sourceFile: String(sourceFile || ''),
      importedAt: new Date().toISOString(),
      questions,
    };
    // 先取索引再落题库文件：反序会让 _readIndex 的孤儿恢复把刚写入的文件先补进索引，
    // 随后 push 又加一条，同一题库在索引里出现两遍（已踩坑）
    const idx = this._readIndex();
    this._writeJsonAtomic(this._bankPath(id), bank);
    idx.banks.push({
      id,
      name: bankName,
      sourceFile: bank.sourceFile,
      importedAt: bank.importedAt,
      count: questions.length,
      enabled: true,
    });
    this._writeIndex(idx);
    return { id, name: bankName, count: questions.length, dropped, enabled: true };
  }

  remove(id) {
    assertId(id);
    const idx = this._readIndex();
    idx.banks = idx.banks.filter((b) => b.id !== id);
    this._writeIndex(idx); // 先摘索引再删文件：中途失败最多留孤儿（可恢复）
    try {
      fs.unlinkSync(this._bankPath(id));
    } catch (_e) {
      /* 已不存在 */
    }
  }

  rename(id, name) {
    assertId(id);
    const idx = this._readIndex();
    const b = idx.banks.find((x) => x.id === id);
    const next = (String(name == null ? '' : name).trim() || (b && b.name) || '').slice(
      0,
      NAME_MAX
    );
    if (!next) return;
    if (b) {
      b.name = next;
      this._writeIndex(idx);
    }
    const bank = this._readBank(id);
    if (bank) {
      bank.name = next;
      this._writeJsonAtomic(this._bankPath(id), bank);
    }
  }

  setEnabled(id, enabled) {
    assertId(id);
    const idx = this._readIndex();
    const b = idx.banks.find((x) => x.id === id);
    if (b && b.enabled !== !!enabled) {
      b.enabled = !!enabled;
      this._writeIndex(idx);
    }
  }

  // 全部启用库的合并题目（每题带上库名，结果窗展示来源）
  allEnabledQuestions() {
    const out = [];
    for (const meta of this.list()) {
      if (!meta.enabled) continue;
      const bank = this._readBank(meta.id);
      if (!bank) continue;
      for (const q of bank.questions) out.push(Object.assign({ bankName: bank.name }, q));
    }
    return out;
  }
}

module.exports = { BankStore, mapHeaders, assertId, ID_RE };
