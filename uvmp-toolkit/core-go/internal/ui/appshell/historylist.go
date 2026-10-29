package appshell

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// HistoryList 最近执行记录列表（对应桌面壳 HistoryCard.vue）：
// 时间/状态/摘要行 + 操作（日志 / 打开目录 / 删除），表头「清空」；
// 删除与清空均经 OnDelete / OnClear 交由调用方走 Backend.DeleteHistory。
type HistoryList struct {
	Root fyne.CanvasObject

	records []ui.HistoryRecord
	format  func(map[string]any) string
	showDir bool
	list    *widget.List
	listBox *minBox
	empty   *widget.Label

	OnLog     func(rec ui.HistoryRecord)
	OnOpenDir func(rec ui.HistoryRecord)
	OnDelete  func(rec ui.HistoryRecord)
	OnClear   func()
}

// NewHistoryList 构造记录卡。format 为任务 result 摘要格式化（nil 时只显示 error）。
func NewHistoryList(title string, showDir bool, format func(map[string]any) string) *HistoryList {
	h := &HistoryList{format: format, showDir: showDir}
	h.empty = muted("暂无执行记录")

	h.list = widget.NewList(
		func() int { return len(h.records) },
		func() fyne.CanvasObject {
			status := widget.NewLabel("")
			timeL := widget.NewLabel("")
			timeL.Importance = widget.LowImportance
			summary := widget.NewLabel("")
			summary.Wrapping = fyne.TextWrapOff
			logBtn := newMotionButton("日志", nil)
			logBtn.Importance = widget.LowImportance
			delBtn := newMotionButton("删除", nil)
			delBtn.Importance = widget.DangerImportance
			dirBtn := newMotionButton("打开目录", nil)
			dirBtn.Importance = widget.LowImportance
			ops := container.NewHBox(logBtn, dirBtn, delBtn)
			left := container.NewVBox(
				container.NewHBox(timeL, status),
				summary,
			)
			return container.NewBorder(nil, nil, nil, ops, left)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id >= len(h.records) {
				return
			}
			rec := h.records[id]
			row := obj.(*fyne.Container)
			left := row.Objects[0].(*fyne.Container)
			ops := row.Objects[1].(*fyne.Container)

			head := left.Objects[0].(*fyne.Container)
			timeL := head.Objects[0].(*widget.Label)
			status := head.Objects[1].(*widget.Label)
			summary := left.Objects[1].(*widget.Label)

			timeL.SetText(rec.Time)
			stText, stImp := historyStatusStyle(rec)
			status.TextStyle = fyne.TextStyle{Bold: true}
			status.Importance = stImp
			status.SetText(stText)

			text := rec.Error
			if text == "" && h.format != nil {
				text = h.format(rec.Result)
			}
			summary.SetText(text)

			logBtn := ops.Objects[0].(*widget.Button)
			dirBtn := ops.Objects[1].(*widget.Button)
			delBtn := ops.Objects[2].(*widget.Button)
			logBtn.OnTapped = func() {
				animateControlTap(logBtn)
				if h.OnLog != nil {
					h.OnLog(rec)
				}
			}
			dirBtn.OnTapped = func() {
				animateControlTap(dirBtn)
				if h.OnOpenDir != nil {
					h.OnOpenDir(rec)
				}
			}
			if !h.showDir || rec.Dir == "" {
				dirBtn.Hide()
			} else {
				dirBtn.Show()
			}
			delBtn.OnTapped = func() {
				animateControlTap(delBtn)
				if h.OnDelete != nil {
					h.OnDelete(rec)
				}
			}
		},
	)
	clearBtn := newMotionButton("清空", nil)
	clearBtn.Importance = widget.DangerImportance
	clearBtn.OnTapped = func() {
		animateControlTap(clearBtn)
		if h.OnClear != nil {
			h.OnClear()
		}
	}
	header := container.NewBorder(nil, nil, secTitle(title), clearBtn)

	// 有记录时保留完整列表视口，避免整页滚动条与只有一行高的内层列表互相争夺。
	h.listBox = newMinBox(fyne.NewSize(0, 224), h.list)
	h.listBox.Hide()
	h.Root = container.NewVBox(header, h.listBox, h.empty)
	return h
}

// SetRecords 刷新数据（可在任意线程调用）。
func (h *HistoryList) SetRecords(recs []ui.HistoryRecord) {
	runUI(func() {
		h.records = recs
		if len(recs) == 0 {
			h.listBox.Hide()
			h.empty.Show()
		} else {
			h.listBox.Show()
			h.empty.Hide()
		}
		h.list.Refresh()
		h.Root.Refresh()
		reflowInScroll(h.Root)
	})
}

// historyStatusStyle 状态列文案与配色（result.paused=true 显示「已暂停」浅蓝语义）。
func historyStatusStyle(rec ui.HistoryRecord) (string, widget.Importance) {
	if rec.Paused() {
		return "已暂停", widget.MediumImportance
	}
	switch rec.Status {
	case ui.JobSuccess:
		return "成功", widget.SuccessImportance
	case ui.JobFailed:
		return "失败", widget.DangerImportance
	case ui.JobCancelled:
		return "已取消", widget.LowImportance
	default:
		return rec.Status.CN(), widget.MediumImportance
	}
}
