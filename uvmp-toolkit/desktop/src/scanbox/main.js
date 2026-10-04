// 扫描框窗交互：拖动（框体）/缩放（右下角柄）/隐藏（×）。
// 只向主进程报告「按下开始(move/resize)/抬起结束」——位移量由主进程按 OS 光标轮询得出
// （渲染层 screenX/movementX 在悬浮窗上不可靠，合成输入下恒为 0——已踩坑）。
// pointercancel/lostpointercapture/blur 一律兜底 dragEnd，防状态卡死。
const box = window.scanbox.boxId
document.getElementById('tag').textContent = '框' + box

let active = false

function begin(e, mode) {
  active = true
  window.scanbox.dragStart(box, mode)
  e.preventDefault()
  e.stopPropagation()
}

function onUp() {
  if (!active) return
  active = false
  window.scanbox.dragEnd(box)
}

const frame = document.getElementById('frame')
const grip = document.getElementById('grip')
frame.addEventListener('pointerdown', (e) => begin(e, 'move'))
grip.addEventListener('pointerdown', (e) => begin(e, 'resize'))
frame.addEventListener('pointerup', onUp)
grip.addEventListener('pointerup', onUp)
frame.addEventListener('pointercancel', onUp)
grip.addEventListener('pointercancel', onUp)
window.addEventListener('blur', onUp)
document.addEventListener('pointercancel', onUp)

const close = document.getElementById('close')
close.addEventListener('pointerdown', (e) => e.stopPropagation())
close.addEventListener('click', () => window.scanbox.hide(box))
