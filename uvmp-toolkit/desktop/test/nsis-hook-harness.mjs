// NSIS 安装钩子的测试装置：解析 build/installer.nsh 的宏，按 NSIS 子集语义执行，
// 并用「假 Windows」实现 tasklist / findstr / taskkill / schtasks。
// 目的：在没有 Windows、也没有 makensis 的机器上，仍能验证钩子触发时机、
// 进程回收的判定与失败分支、以及安装明细开关。
// 注意：这是测试替身，不代表已在 Windows 真机跑过 Makensis 编译。

import fs from 'node:fs';

const SYSDIR = 'C:\\Windows';
const INSTDIR = 'C:\\Users\\tester\\AppData\\Local\\Programs\\uvmp-toolkit-desktop';

// ---------------------------------------------------------------------------
// 脚本解析
// ---------------------------------------------------------------------------

export function parseNsis(text) {
  const macros = new Map();
  const defines = new Map();
  let current = null;
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith(';')) continue;
    const start = /^!macro\s+(\S+)(?:\s+(.*))?$/.exec(line);
    if (start) {
      current = {
        name: start[1],
        params: start[2] ? start[2].split(/\s+/).filter(Boolean) : [],
        body: [],
      };
      macros.set(current.name, current);
      continue;
    }
    if (/^!macroend$/i.test(line)) {
      current = null;
      continue;
    }
    if (current) {
      current.body.push(line);
      continue;
    }
    const def = /^!define\s+(\S+)(?:\s+(.*))?$/.exec(line);
    if (def) defines.set(def[1], def[2] == null ? '' : def[2]);
  }
  return { macros, defines };
}

export function loadInstallerScript(path) {
  return parseNsis(fs.readFileSync(path, 'utf8'));
}

// ---------------------------------------------------------------------------
// 宏展开：替换 ${参数} / ${define} / ${__LINE__}，处理 !ifdef / !ifndef / !define / !undef，
// 并递归展开 !insertmacro。参数必须是单 token（NSIS 参数按空格切分，这条限制要显式测）。
// ---------------------------------------------------------------------------

export function expandMacro(
  script,
  name,
  { args = [], defines = [], values = {}, lineSeed = 100 } = {}
) {
  const state = {
    defines: new Map([...script.defines, ...Object.entries(values)]),
    extra: new Set(defines),
    line: lineSeed,
    out: [],
    depth: 0,
  };
  const has = (key) => state.defines.has(key) || state.extra.has(key);
  const expandText = (text) =>
    text.replace(/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/g, (whole, key) => {
      if (key === '__LINE__') return String(state.line++);
      if (state.defines.has(key)) return state.defines.get(key);
      return whole; // LogicLib 的 ${If} / ${Silent} 等留给执行器
    });

  function walk(macroName, macroArgs) {
    const macro = script.macros.get(macroName);
    if (!macro) throw new Error(`未找到宏 ${macroName}`);
    if (macro.params.length !== macroArgs.length) {
      throw new Error(
        `宏 ${macroName} 期望 ${macro.params.length} 个参数，收到 ${macroArgs.length} 个：${JSON.stringify(macroArgs)}`
      );
    }
    const params = new Map(macro.params.map((p, i) => [p, macroArgs[i]]));
    const substitute = (text) => {
      let result = text;
      for (const [key, value] of params) result = result.split('${' + key + '}').join(value);
      return expandText(result);
    };
    const conditions = [];
    const active = () => conditions.every(Boolean);
    for (const line of macro.body) {
      const ifdef = /^!ifdef\s+(\S+)$/.exec(line);
      if (ifdef) {
        conditions.push(active() && has(substitute(ifdef[1])));
        continue;
      }
      const ifndef = /^!ifndef\s+(\S+)$/.exec(line);
      if (ifndef) {
        conditions.push(active() && !has(substitute(ifndef[1])));
        continue;
      }
      if (/^!endif$/.test(line)) {
        conditions.pop();
        continue;
      }
      if (!active()) continue;
      const undef = /^!undef\s+(\S+)$/.exec(line);
      if (undef) {
        state.defines.delete(substitute(undef[1]));
        continue;
      }
      const def = /^!define\s+(\S+)(?:\s+(.*))?$/.exec(line);
      if (def) {
        state.defines.set(substitute(def[1]), def[2] == null ? '' : substitute(def[2]));
        continue;
      }
      const nested = /^!insertmacro\s+(\S+)(?:\s+(.*))?$/.exec(line);
      if (nested) {
        const rest = nested[2] == null ? '' : substitute(nested[2]).trim();
        const nestedArgs = rest ? rest.split(/\s+/) : [];
        for (const arg of nestedArgs) {
          if (/\s/.test(arg)) throw new Error(`宏 ${nested[1]} 的参数含空格：${arg}`);
        }
        walk(nested[1], nestedArgs);
        continue;
      }
      if (/^!include\b/.test(line)) continue;
      state.out.push(substitute(line));
    }
  }

  state.extra = new Set([...state.extra, ...defines]);
  walk(name, args);
  return state.out;
}

// ---------------------------------------------------------------------------
// 假 Windows：进程表 + tasklist/findstr/taskkill/schtasks 替身
// ---------------------------------------------------------------------------

function csvLine(proc) {
  return `"${proc.name}","${proc.pid}","Console","1","12,345 K"`;
}

export function createFakeWindows(processes) {
  const procs = processes.map((p) => ({
    name: p.name,
    pid: p.pid,
    stubborn: Boolean(p.stubborn), // /F 也杀不掉（模拟权限不足）
    softStubborn: Boolean(p.softStubborn), // 温和关闭无效，/F 有效（模拟关闭到托盘）
  }));
  const calls = [];

  function find(filter) {
    return procs.filter((p) => p.name.toLowerCase() === filter.toLowerCase());
  }

  function runTasklistFindstr(cmd) {
    const filterMatch = /IMAGENAME eq ([^"\s]+)/i.exec(cmd);
    if (!filterMatch) throw new Error('未解析到 IMAGENAME 过滤条件：' + cmd);
    const anchorRaw = cmd
      .slice(cmd.indexOf('/C:') + 3)
      .trim()
      .replace(/^"|"$/g, '')
      .replace(/\\"/g, '"');
    // findstr 的模式是带引号的完整首字段（"uvmp-toolkit.exe"），即 CSV 行的行首。
    const anchor = anchorRaw;
    if (anchor.replace(/"/g, '').toLowerCase() !== filterMatch[1].toLowerCase()) {
      throw new Error(`findstr 锚点 ${anchor} 与 tasklist 过滤条件 ${filterMatch[1]} 不一致`);
    }
    const lines = find(filterMatch[1]).map(csvLine);
    const hit = lines.filter((line) => line.toUpperCase().startsWith(anchor.toUpperCase()));
    calls.push({ bin: 'findstr', filter: filterMatch[1], anchor, hits: hit.length });
    return { rc: hit.length > 0 ? 0 : 1, stdout: hit.join('\r\n') };
  }

  function runTaskkill(cmd) {
    const imageMatch = /\/IM\s+"?([^"\s]+)"?/i.exec(cmd);
    if (!imageMatch) throw new Error('taskkill 缺少 /IM：' + cmd);
    const image = imageMatch[1];
    if (image.includes('*')) throw new Error('出现了通配符杀进程：' + image);
    const force = /\/F\b/.test(cmd);
    const targets = find(image);
    calls.push({ bin: 'taskkill', image, force, hits: targets.length });
    if (targets.length === 0) return { rc: 128, stdout: 'ERROR: The process not found.' };
    let rc = 0;
    for (const target of targets) {
      if (target.stubborn) {
        rc = 1;
        continue;
      }
      if (!force && target.softStubborn) {
        rc = 1;
        continue;
      }
      procs.splice(procs.indexOf(target), 1);
    }
    return { rc, stdout: '' };
  }

  return {
    procs,
    calls,
    names: () => procs.map((p) => p.name),
    setStubborn(name, value) {
      for (const p of procs) if (p.name.toLowerCase() === name.toLowerCase()) p.stubborn = value;
    },
    run(cmd) {
      const resolved = cmd.replace(/\$SYSDIR/g, SYSDIR).replace(/\$INSTDIR/g, INSTDIR);
      if (/tasklist/i.test(resolved) && /findstr/i.test(resolved))
        return runTasklistFindstr(resolved);
      if (/taskkill/i.test(resolved)) return runTaskkill(resolved);
      if (/schtasks/i.test(resolved)) {
        calls.push({ bin: 'schtasks', cmd: resolved });
        return { rc: 0, stdout: 'SUCCESS' };
      }
      throw new Error('未识别的假命令：' + resolved);
    },
  };
}

// ---------------------------------------------------------------------------
// 执行器：NSIS 子集（LogicLib 的 If/IfNot/AndIf/AndIfNot/EndIf、Goto 标签、
// nsExec、MessageBox、DetailPrint、StrCpy、IntOp、Push/Pop、Quit 等）
// ---------------------------------------------------------------------------

function stripQuotes(text) {
  const t = text.trim();
  return t.startsWith('"') && t.endsWith('"') ? t.slice(1, -1) : t;
}

function decodeText(text) {
  return text
    .replace(/\$\\r\$\\n/g, '\n')
    .replace(/\$\\r/g, '\r')
    .replace(/\$\\n/g, '\n')
    .replace(/\$\\"/g, '"')
    .replace(/\$\$/g, '$');
}

function interpolate(text, vars) {
  return decodeText(text).replace(/\$(R\d|\d|INSTDIR|SYSDIR)/g, (whole, key) => {
    if (key === 'SYSDIR') return SYSDIR;
    if (key === 'INSTDIR') return INSTDIR;
    return vars.has('$' + key) ? vars.get('$' + key) : whole;
  });
}

function resolveValue(token, vars) {
  const t = token.trim();
  if (t === '""') return '';
  if (t.startsWith('"')) return interpolate(stripQuotes(t), vars);
  if (t.startsWith('$')) return vars.get(t) ?? '';
  return t;
}

export function createState({ answers = [], runtimeFlags = {}, fake, onMessage } = {}) {
  return {
    vars: new Map(),
    stack: [],
    details: [],
    commands: [],
    messages: [],
    showInstDetails: null,
    showUninstDetails: null,
    detailsView: [],
    detailsPrint: [],
    errorLevel: 0,
    quit: false,
    answers: [...answers],
    runtimeFlags: { Silent: false, isUpdated: false, ...runtimeFlags },
    fake,
    onMessage,
  };
}

// 扫描当前 ${If} 对应的 ${Else}（同层）与 ${EndIf}，供分支跳转使用。
function findBlock(lines, index) {
  let depth = 0;
  let elseAt = -1;
  for (let i = index; i < lines.length; i += 1) {
    if (/^\$\{(If|IfNot)\}/.test(lines[i])) depth += 1;
    else if (/^\$\{ElseIf\}/.test(lines[i]) && depth === 1) {
      throw new Error('测试装置未实现 ${ElseIf}，请改用 ${Else}');
    } else if (/^\$\{Else\}$/.test(lines[i]) && depth === 1) {
      elseAt = i;
    } else if (/^\$\{EndIf\}$/.test(lines[i])) {
      depth -= 1;
      if (depth === 0) return { elseAt, endIf: i };
    }
  }
  throw new Error('未找到匹配的 ${EndIf}');
}

function evaluate(condition, state) {
  const text = condition.trim();
  const flag = /^\$\{(\w+)\}$/.exec(text);
  if (flag) return state.runtimeFlags[flag[1]] === true;
  const numeric = /^(.+?)\s*(<=|>=|<|>)\s*(.+)$/.exec(text);
  if (numeric) {
    const left = Number(resolveValue(numeric[1], state.vars));
    const right = Number(resolveValue(numeric[3], state.vars));
    if (!Number.isFinite(left) || !Number.isFinite(right)) {
      throw new Error('数值比较的操作数不是数字：' + text);
    }
    switch (numeric[2]) {
      case '<':
        return left < right;
      case '>':
        return left > right;
      case '<=':
        return left <= right;
      default:
        return left >= right;
    }
  }
  const compare = /^(.+?)\s*(==|!=)\s*(.+)$/.exec(text);
  if (compare) {
    const left = resolveValue(compare[1], state.vars);
    const right = resolveValue(compare[3], state.vars);
    return compare[2] === '==' ? left === right : left !== right;
  }
  throw new Error('无法求值的条件：' + text);
}

export function executeLines(lines, state) {
  const labels = new Map();
  lines.forEach((line, index) => {
    const label = /^([\w.$]+):$/.exec(line);
    if (label) labels.set(label[1], index);
  });

  let ip = 0;
  let steps = 0;
  const blocks = []; // 每层 ${If} 的 {elseAt, endIf, taken}
  while (ip < lines.length) {
    steps += 1;
    if (steps > 20000) throw new Error('执行步数超限，疑似死循环');
    const line = lines[ip];

    if (/^[\w.$]+:$/.test(line) || /^!include\b/.test(line)) {
      ip += 1;
      continue;
    }

    const branch = /^\$\{(IfNot|If|AndIfNot|AndIf)\}\s+(.+)$/.exec(line);
    if (branch) {
      let ok = evaluate(branch[2], state);
      if (branch[1].endsWith('Not')) ok = !ok;
      // AndIf / AndIfNot 属于同一层条件，不新开块。
      if (branch[1].startsWith('And')) {
        if (!ok && blocks.length > 0) {
          const top = blocks[blocks.length - 1];
          if (top.elseAt >= 0) ip = top.elseAt + 1;
          else {
            blocks.pop();
            ip = top.endIf + 1;
          }
        } else {
          ip += 1;
        }
        continue;
      }
      const block = { ...findBlock(lines, ip), taken: ok };
      if (ok) {
        blocks.push(block);
        ip += 1;
      } else if (block.elseAt >= 0) {
        blocks.push(block);
        ip = block.elseAt + 1;
      } else {
        ip = block.endIf + 1;
      }
      continue;
    }

    if (/^\$\{Else\}$/.test(line)) {
      const block = blocks.pop();
      if (!block) throw new Error('多余的 ${Else}');
      ip = block.endIf + 1;
      continue;
    }

    if (/^\$\{EndIf\}$/.test(line)) {
      blocks.pop();
      ip += 1;
      continue;
    }

    const goto = /^Goto\s+(\S+)$/i.exec(line);
    if (goto) {
      if (!labels.has(goto[1])) throw new Error('未找到标签：' + goto[1]);
      ip = labels.get(goto[1]);
      continue;
    }

    const push = /^Push\s+(.+)$/i.exec(line);
    if (push) {
      state.stack.push(resolveValue(push[1], state.vars));
      ip += 1;
      continue;
    }

    const pop = /^Pop\s+(\$\S+)$/i.exec(line);
    if (pop) {
      if (state.stack.length === 0) throw new Error('栈为空时执行 Pop ' + pop[1]);
      state.vars.set(pop[1], state.stack.pop());
      ip += 1;
      continue;
    }

    const strcpy = /^StrCpy\s+(\$\S+)\s+(.+)$/i.exec(line);
    if (strcpy) {
      state.vars.set(strcpy[1], resolveValue(strcpy[2], state.vars));
      ip += 1;
      continue;
    }

    const intop = /^IntOp\s+(\$\S+)\s+(\$\S+)\s*([+\-*/])\s*(\d+)$/i.exec(line);
    if (intop) {
      const left = Number(state.vars.get(intop[2]) ?? 0);
      const right = Number(intop[4]);
      const value =
        intop[3] === '+'
          ? left + right
          : intop[3] === '-'
            ? left - right
            : intop[3] === '*'
              ? left * right
              : left / right;
      state.vars.set(intop[1], String(value));
      ip += 1;
      continue;
    }

    const detail = /^DetailPrint\s+"(.*)"$/i.exec(line);
    if (detail) {
      state.details.push(interpolate(detail[1], state.vars));
      ip += 1;
      continue;
    }

    const showInst = /^ShowInstDetails\s+(\S+)$/i.exec(line);
    if (showInst) {
      state.showInstDetails = showInst[1];
      ip += 1;
      continue;
    }

    const showUninst = /^ShowUninstDetails\s+(\S+)$/i.exec(line);
    if (showUninst) {
      state.showUninstDetails = showUninst[1];
      ip += 1;
      continue;
    }

    const view = /^SetDetailsView\s+(\S+)$/i.exec(line);
    if (view) {
      state.detailsView.push(view[1]);
      ip += 1;
      continue;
    }

    const setPrint = /^SetDetailsPrint\s+(\S+)$/i.exec(line);
    if (setPrint) {
      state.detailsPrint.push(setPrint[1]);
      ip += 1;
      continue;
    }

    const sleep = /^Sleep\s+\d+$/i.exec(line);
    if (sleep) {
      ip += 1;
      continue;
    }

    const nsExec = /^nsExec::(Exec|ExecToLog)\s+(.+)$/i.exec(line);
    if (nsExec) {
      const raw = nsExec[2].trim();
      const command = raw.startsWith('`') ? raw.slice(1, -1) : stripQuotes(raw);
      const resolved = interpolate(command, state.vars);
      state.commands.push(resolved);
      const result = state.fake.run(resolved);
      if (nsExec[1] === 'ExecToLog' && result.stdout) state.details.push(result.stdout);
      state.stack.push(String(result.rc));
      ip += 1;
      continue;
    }

    const messageBox = /^MessageBox\s+(\S+)\s+"([^"]*)"\s*(.*)$/i.exec(line);
    if (messageBox) {
      const flags = messageBox[1];
      const text = decodeText(messageBox[2]);
      const tail = messageBox[3].trim();
      let answer;
      if (state.runtimeFlags.Silent) {
        const silentAnswer = /\/SD\s+(\S+)/i.exec(tail);
        answer = silentAnswer ? silentAnswer[1].toUpperCase() : 'IDOK';
      } else {
        if (state.answers.length === 0) throw new Error('测试未预设 MessageBox 答案：' + text);
        answer = state.answers.shift().toUpperCase();
      }
      state.messages.push({ flags, text, answer });
      if (state.onMessage) state.onMessage({ flags, text, answer, state });
      // MB_RETRYCANCEL 的「重试」按钮返回 IDRETRY；测试里写 IDOK 也按「重试」处理。
      const retryBox = /MB_RETRYCANCEL/.test(flags);
      if (
        retryBox &&
        (answer === 'IDRETRY' || answer === 'IDOK') &&
        /\bIDRETRY\s+(\S+)/i.test(tail)
      ) {
        const target = /\bIDRETRY\s+(\S+)/i.exec(tail)[1];
        if (!labels.has(target)) throw new Error('MessageBox 目标标签不存在：' + target);
        ip = labels.get(target);
        continue;
      }
      if (!retryBox && answer === 'IDOK' && /\bIDOK\s+(\S+)/i.test(tail)) {
        const target = /\bIDOK\s+(\S+)/i.exec(tail)[1];
        if (!labels.has(target)) throw new Error('MessageBox 目标标签不存在：' + target);
        ip = labels.get(target);
        continue;
      }
      ip += 1; // IDCANCEL / IDNO 等：落到下一条（调用方随后 Quit）
      continue;
    }

    const setError = /^SetErrorLevel\s+(\d+)$/i.exec(line);
    if (setError) {
      state.errorLevel = Number(setError[1]);
      ip += 1;
      continue;
    }

    if (/^Quit$/i.test(line)) {
      state.quit = true;
      return state;
    }

    if (/^(FileOpen|FileWrite|FileClose)\b/i.test(line)) {
      ip += 1;
      continue;
    }

    throw new Error('测试装置未实现的指令：' + line);
  }
  return state;
}

export function runMacro(script, name, options = {}) {
  const { defines = [], values = {}, args = [], ...rest } = options;
  const lines = expandMacro(script, name, { args, defines: new Set(defines), values });
  const state = createState(rest);
  executeLines(lines, state);
  return { state, lines };
}
