// 结果窗渲染：按框 A/B 分区展示匹配结果，答案选项标红；仅开一个扫描框时不分区（不显示框标）；
// 标题栏拖动移动、右下角柄缩放（只报起止，位移由主进程轮询 OS 光标得出）。
const LETTERS = ['A', 'B', 'C', 'D', 'E', 'F']

function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

function renderSec(el, boxId, st, showTag) {
  const tag = showTag ? '<span class="boxtag">框' + boxId + '</span>' : ''
  if (!st) {
    el.innerHTML = '<div class="boxline">' + tag + '<span class="none">等待扫描…</span></div>'
    return
  }
  if (!st.matched) {
    const snip = st.ocrText ? '<div class="ocrsnip">识别到：' + esc(st.ocrText.slice(0, 40)) + '</div>' : ''
    el.innerHTML = '<div class="boxline">' + tag + '<span class="none">未匹配到题目</span></div>' + snip
    return
  }
  const q = st.q
  const answer = String(q.answer || '').toUpperCase()
  let opts = ''
  if (q.type !== 'judge' || (q.options && q.options.length > 2)) {
    const parts = (q.options || []).map((o, i) => {
      const L = LETTERS[i] || String(i + 1)
      const hit = answer.includes(L)
      return '<span class="opt' + (hit ? ' hit' : '') + '">' + L + '. ' + esc(o) + '</span>'
    })
    opts = '<div class="opts">' + parts.join('') + '</div>'
  } else {
    const hitText = answer === 'A' ? '正确' : '错误'
    opts = '<div class="opts"><span class="opt hit">' + esc(hitText) + '</span></div>'
  }
  el.innerHTML =
    '<div class="boxline">' + tag +
    '<span class="ans">' + esc(answer) + '</span>' +
    '<span class="meta">' + esc(q.bankName || '') + ' · 匹配度 ' + Math.round((st.score || 0) * 100) + '%</span>' +
    '</div>' +
    '<div class="stem">' + esc(q.content) + '</div>' +
    opts
}

const secA = document.getElementById('secA')
const secB = document.getElementById('secB')
const ms = document.getElementById('ms')

window.resultPanel.onState((payload) => {
  const boxes = payload.boxes || {}
  const vis = payload.visible || { A: true, B: true }
  const enabled = ['A', 'B'].filter((id) => vis[id])
  if (enabled.length >= 2) {
    // 双框：分区展示（含框标与分隔线）
    secA.style.display = ''
    secB.style.display = ''
    renderSec(secA, 'A', boxes.A, true)
    renderSec(secB, 'B', boxes.B, true)
  } else {
    // 单框：整窗只显示该框结果，不分区不带框标
    const id = enabled[0] || 'A'
    secA.style.display = ''
    secB.style.display = 'none'
    renderSec(secA, id, boxes[id], false)
  }
  ms.textContent = payload.ocrMs ? 'OCR ' + payload.ocrMs + 'ms' : ''
})

// 标题栏拖动移动（只报起止；位移由主进程轮询 OS 光标得出——渲染层坐标不可靠，已踩坑）
const head = document.querySelector('.head')
const grip = document.getElementById('grip')
let dragging = false

function dragBegin(e, mode) {
  dragging = true
  window.resultPanel.dragStart(mode)
  e.preventDefault()
  e.stopPropagation()
}
function dragDone() {
  if (!dragging) return
  dragging = false
  window.resultPanel.dragEnd()
}
head.addEventListener('pointerdown', (e) => dragBegin(e, 'move'))
grip.addEventListener('pointerdown', (e) => dragBegin(e, 'resize'))
head.addEventListener('pointerup', dragDone)
head.addEventListener('pointercancel', dragDone)
grip.addEventListener('pointerup', dragDone)
grip.addEventListener('pointercancel', dragDone)
window.addEventListener('blur', dragDone)
