// 题库本地存储：原子写、损坏保留、孤儿恢复、ID 白名单。
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const { BankStore } = require('../electron/quizsearch/bank-store.cjs');

const TEMP_DIRS = [];
test.after(() => {
  for (const dir of TEMP_DIRS) fs.rmSync(dir, { recursive: true, force: true });
});

function tmpDir() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'quiz-bank-'));
  TEMP_DIRS.push(dir);
  return dir;
}

function bankFile(dir, id, name, content) {
  fs.mkdirSync(path.join(dir, 'banks'), { recursive: true });
  fs.writeFileSync(
    path.join(dir, 'banks', id + '.json'),
    JSON.stringify({
      id,
      name,
      importedAt: '2026-01-02T03:04:05.000Z',
      questions: [
        { type: 'single', content, options: ['甲', '乙'], answer: 'A', analysis: '', sort: 0 },
      ],
    }),
    'utf8'
  );
}

const ROWS = [
  ['题干', '答案', '选项A', '选项B', '题型', '解析'],
  ['我国安全生产工作的方针是？', 'A', '安全第一、预防为主、综合治理', '质量第一', '单选', '解析一'],
  ['安全生产，人人有责。', 'A', '正确', '错误', '判断', ''],
  ['', 'A', '空题干应丢弃', '', '单选', ''],
  ['我国安全生产工作的方针是？', 'A', '安全第一、预防为主、综合治理', '质量第一', '单选', '重复行'],
];

test('导入：解析题干/答案/选项/题型，丢弃无效与重复行', () => {
  const dir = tmpDir();
  const store = new BankStore(dir);
  const r = store.importRows('安规题库', '/tmp/安规.xlsx', ROWS);
  assert.equal(r.count, 2);
  assert.equal(r.dropped, 2);

  const bank = store.getBank(r.id);
  assert.equal(bank.name, '安规题库');
  assert.equal(bank.questions.length, 2);
  assert.equal(bank.questions[0].type, 'single');
  assert.deepEqual(bank.questions[0].options, ['安全第一、预防为主、综合治理', '质量第一']);
  assert.equal(bank.questions[1].type, 'judge');
  assert.equal(bank.questions[1].answer, 'A');
  assert.deepEqual(bank.questions[1].options, ['正确', '错误']);

  const list = store.list();
  assert.equal(list.length, 1);
  assert.equal(list[0].id, r.id);
  assert.equal(list[0].count, 2);
  assert.equal(list[0].enabled, true);

  // 索引文件必须是完整 JSON（原子写不留半截），且无临时文件残留
  assert.equal(
    JSON.parse(fs.readFileSync(path.join(dir, 'banks-index.json'), 'utf8')).banks.length,
    1
  );
  assert.deepEqual(
    fs.readdirSync(dir).filter((f) => f.includes('.tmp')),
    []
  );
});

test('表头缺列时报中文错误，不落任何题库文件', () => {
  const dir = tmpDir();
  const store = new BankStore(dir);
  assert.throws(
    () =>
      store.importRows('x', 'x.xlsx', [
        ['题目', '选项A'],
        ['a', 'b'],
      ]),
    /表头/
  );
  assert.throws(() => store.importRows('x', 'x.xlsx', []), /为空/);
  assert.deepEqual(
    fs.readdirSync(path.join(dir, 'banks')).filter((f) => f.endsWith('.json')),
    []
  );
});

test('索引损坏：保留损坏文件并给出提示，同时从题库文件恢复', () => {
  const dir = tmpDir();
  bankFile(dir, 'abc123', '恢复题库', '恢复题目');
  const indexPath = path.join(dir, 'banks-index.json');
  fs.writeFileSync(indexPath, '{"banks":[{"id":"abc123"', 'utf8'); // 半截 JSON

  const store = new BankStore(dir);
  const list = store.list();
  assert.equal(list.length, 1);
  assert.equal(list[0].id, 'abc123');
  assert.equal(list[0].count, 1);
  assert.match(store.notice, /索引/);

  const backups = fs.readdirSync(dir).filter((f) => f.startsWith('banks-index.corrupt-'));
  assert.equal(backups.length, 1, '损坏索引必须留档，不能静默丢弃');
  assert.match(fs.readFileSync(path.join(dir, backups[0]), 'utf8'), /abc123/);
  assert.equal(JSON.parse(fs.readFileSync(indexPath, 'utf8')).banks.length, 1);
});

test('孤儿题库文件：无索引条目时自动补回索引', () => {
  const dir = tmpDir();
  fs.writeFileSync(path.join(dir, 'banks-index.json'), JSON.stringify({ banks: [] }), 'utf8');
  bankFile(dir, 'orphan1', '孤儿题库', '孤儿题目');
  const store = new BankStore(dir);
  const list = store.list();
  assert.deepEqual(
    list.map((b) => b.id),
    ['orphan1']
  );
  assert.equal(list[0].count, 1);
  assert.equal(
    JSON.parse(fs.readFileSync(path.join(dir, 'banks-index.json'), 'utf8')).banks[0].id,
    'orphan1'
  );
});

test('索引条目指向的题库文件缺失：从索引剔除', () => {
  const dir = tmpDir();
  fs.writeFileSync(
    path.join(dir, 'banks-index.json'),
    JSON.stringify({ banks: [{ id: 'gone1', name: '缺失', count: 3, enabled: true }] }),
    'utf8'
  );
  const store = new BankStore(dir);
  assert.deepEqual(store.list(), []);
  assert.deepEqual(
    JSON.parse(fs.readFileSync(path.join(dir, 'banks-index.json'), 'utf8')).banks,
    []
  );
});

test('题库 ID 白名单：拒绝路径穿越与非法字符', () => {
  const dir = tmpDir();
  bankFile(dir, 'ok01', '正常题库', '题目');
  fs.writeFileSync(path.join(dir, 'evil.json'), JSON.stringify({ id: 'evil' }), 'utf8');
  const store = new BankStore(dir);

  const bad = ['../evil', '..\\evil', '/etc/passwd', 'a/b', '', 'x'.repeat(65), 'id with space'];
  for (const id of bad) {
    assert.throws(() => store.remove(id), /ID/, 'remove ' + JSON.stringify(id));
    assert.throws(() => store.rename(id, '新名'), /ID/, 'rename ' + JSON.stringify(id));
    assert.throws(() => store.setEnabled(id, false), /ID/, 'setEnabled ' + JSON.stringify(id));
    assert.equal(store.getBank(id), null, 'getBank ' + JSON.stringify(id));
  }
  assert.ok(fs.existsSync(path.join(dir, 'evil.json')), '不得读写题库目录之外的文件');
  assert.equal(store.getBank('ok01').name, '正常题库');
});

test('启用状态与重命名落盘，删除同时清理文件与索引', () => {
  const dir = tmpDir();
  const store = new BankStore(dir);
  const r = store.importRows('题库甲', 'a.xlsx', ROWS);
  store.setEnabled(r.id, false);
  assert.equal(store.list()[0].enabled, false);
  assert.equal(store.allEnabledQuestions().length, 0);

  store.rename(r.id, '  题库乙  ');
  assert.equal(store.list()[0].name, '题库乙');
  assert.equal(store.getBank(r.id).name, '题库乙');

  store.remove(r.id);
  assert.deepEqual(store.list(), []);
  assert.equal(fs.existsSync(path.join(dir, 'banks', r.id + '.json')), false);
  // 重启后仍是空索引
  assert.deepEqual(new BankStore(dir).list(), []);
});
