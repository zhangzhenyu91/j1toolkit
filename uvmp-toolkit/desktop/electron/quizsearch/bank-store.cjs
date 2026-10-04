// 题库搜题：题库本地存储（纯 Node，无 Electron 依赖，可 node 直测）。
// 存储布局（dir = userData/quiz-search）：
//   banks-index.json      {banks:[{id,name,sourceFile,importedAt,count,enabled}]}
//   banks/<id>.json       {id,name,questions:[{type,content,options[],answer,analysis}]}
// 字段口径对齐壹匣刷题（server/src/quiz/schema.js quiz_question）：
//   type: single 单选 / multiple 多选 / judge 判断；判断题固定选项 ["正确","错误"]，A=正确 B=错误。
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

function newId() {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 6);
}

// 表头名归一化（去空白/全角转半角/小写）后查别名表
function mapHeaders(headerRow) {
  const colOf = {};
  headerRow.forEach((h, idx) => {
    const key = String(h == null ? '' : h).trim()
      .replace(/[Ａ-Ｚａ-ｚ０-９]/g, (c) => String.fromCharCode(c.charCodeAt(0) - 0xfee0))
      .toLowerCase();
    if (!key) return;
    for (const [field, aliases] of Object.entries(HEADER_ALIASES)) {
      if (aliases.includes(key)) { colOf[field] = idx; return; }
    }
    const m = key.match(/^选项\s*([a-f])$/);
    if (m) { colOf['option' + m[1].toUpperCase()] = idx; return; }
  });
  return colOf;
}

function normAnswer(raw) {
  return String(raw == null ? '' : raw).trim().toUpperCase()
    .replace(/[，、\s]/g, '')
    .split('').sort().join('');
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
    fs.mkdirSync(this.banksDir, { recursive: true });
  }

  _readIndex() {
    try {
      return JSON.parse(fs.readFileSync(this.indexPath, 'utf8'));
    } catch (_e) {
      return { banks: [] };
    }
  }

  _writeIndex(idx) {
    fs.writeFileSync(this.indexPath, JSON.stringify(idx, null, 2), 'utf8');
  }

  list() {
    return this._readIndex().banks;
  }

  getBank(id) {
    try {
      return JSON.parse(fs.readFileSync(path.join(this.banksDir, id + '.json'), 'utf8'));
    } catch (_e) {
      return null;
    }
  }

  /**
   * 导入题库。rows 为 SheetJS sheet_to_json({header:1}) 的二维数组（首行表头）。
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
      if (!content || !answer) { dropped++; continue; }
      const options = [];
      for (const L of OPTION_LETTERS) {
        const c = colOf['option' + L];
        const v = c === undefined ? '' : String(r[c] == null ? '' : r[c]).trim();
        if (v) options.push(v);
      }
      const analysis = colOf.analysis === undefined ? '' : String(r[colOf.analysis] == null ? '' : r[colOf.analysis]).trim();
      let type = colOf.type === undefined ? '' : String(r[colOf.type] == null ? '' : r[colOf.type]).trim().toLowerCase();
      const typeMap = { 单选: 'single', 多选: 'multiple', 判断: 'judge', single: 'single', multiple: 'multiple', judge: 'judge' };
      type = typeMap[type] || '';
      if (!type) {
        if (/^(正确|错误|对|错|√|×|T|F|TRUE|FALSE|YES|NO)$/.test(answer)) {
          type = 'judge';
        } else if (options.length === 0 && (answer === 'A' || answer === 'B')) {
          type = 'judge';   // 无选项且答案仅 A/B：判断题（表内省略了 正确/错误 选项列）
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
      if (seen.has(key)) { dropped++; continue; }   // 库内去重（原软件 Distinct 语义）
      seen.add(key);
      questions.push({ type, content, options, answer, analysis, sort: questions.length });
    }
    if (questions.length === 0) throw new Error('未解析到有效题目，请检查表头与数据');
    const id = newId();
    const bankName = String(name || '').trim() || path.basename(String(sourceFile || '题库'), path.extname(String(sourceFile || '')));
    const bank = { id, name: bankName, sourceFile: String(sourceFile || ''), importedAt: new Date().toISOString(), questions };
    fs.writeFileSync(path.join(this.banksDir, id + '.json'), JSON.stringify(bank), 'utf8');
    const idx = this._readIndex();
    idx.banks.push({ id, name: bankName, sourceFile: bank.sourceFile, importedAt: bank.importedAt, count: questions.length, enabled: true });
    this._writeIndex(idx);
    return { id, name: bankName, count: questions.length, dropped, enabled: true };
  }

  remove(id) {
    const idx = this._readIndex();
    idx.banks = idx.banks.filter((b) => b.id !== id);
    this._writeIndex(idx);
    try { fs.unlinkSync(path.join(this.banksDir, id + '.json')); } catch (_e) { /* 已不存在 */ }
  }

  rename(id, name) {
    const idx = this._readIndex();
    const b = idx.banks.find((x) => x.id === id);
    if (b) { b.name = String(name || '').trim() || b.name; this._writeIndex(idx); }
    const bank = this.getBank(id);
    if (bank) { bank.name = b ? b.name : bank.name; fs.writeFileSync(path.join(this.banksDir, id + '.json'), JSON.stringify(bank), 'utf8'); }
  }

  setEnabled(id, enabled) {
    const idx = this._readIndex();
    const b = idx.banks.find((x) => x.id === id);
    if (b) { b.enabled = !!enabled; this._writeIndex(idx); }
  }

  // 全部启用库的合并题目（每题带上库名，结果窗展示来源）
  allEnabledQuestions() {
    const out = [];
    for (const meta of this.list()) {
      if (!meta.enabled) continue;
      const bank = this.getBank(meta.id);
      if (!bank) continue;
      for (const q of bank.questions) out.push(Object.assign({ bankName: bank.name }, q));
    }
    return out;
  }
}

module.exports = { BankStore, mapHeaders };
