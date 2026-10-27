// Package bridge 跨模块桥接装配：office 文档源能力（wls 工作单 / sdr 安全日 / wlr 备注附件 /
// thd 树障统计表 / crs 交跨统计表，实现落各业务包 officebridge.go）、网盘读写适配
// （worklog/safeday 保存 + kvm 互推，见 netdisk.go）、微信智能应答捕获出口（tree/crossing
// 直推，见 sink.go）与群内提问/聊天归档直调、WF3 命名、PadPro 五通道（ask.go / chatlog.go /
// padpro.go）、内网 U 盘同步设备链路与通知（usb.go / notice.go）、派车单取件建卡（dispatch.go）。
// main 一行 bridge.Wire(...) 注入；不新增路由与存储，全部复用业务包既有导出/查询/COS 回源链路。
package bridge

import (
	"database/sql"

	"shade/internal/apps/callme"
	"shade/internal/apps/chatlog"
	"shade/internal/apps/crossing"
	"shade/internal/apps/kvm"
	"shade/internal/apps/netdisk"
	"shade/internal/apps/office"
	"shade/internal/apps/safeday"
	"shade/internal/apps/treehazard"
	"shade/internal/apps/wechatanswer"
	"shade/internal/apps/worklog"
)

// Deps 桥接装配输入（模块按需置 nil 跳过对应桥）。
type Deps struct {
	Office   *office.Module
	Worklog  *worklog.Module
	Safeday  *safeday.Module
	Tree     *treehazard.Module
	Crossing *crossing.Module
	Kvm      *kvm.Module
	Netdisk  *netdisk.Module
	// Callme / Chatlog / Wechat：群内提问直调（AskIngest）、聊天归档直调（ChatlogSink）、
	// WF3 文件命名（DIFY_WF3_API_KEY）与 WeChatPadPro 五通道集中构造。
	Callme  *callme.Module
	Chatlog *chatlog.Module
	Wechat  *wechatanswer.Module
	// Cfg 运行时配置（CaptureSink 对接密钥来源，与 tree/crossing 模块同源；通知/设备/WF3 同源）。
	Cfg ConfigGet
	// DB 通知与派车单同步 SQL 实现（sys_notice / worklog_*；nil 时通知与派车桥跳过）。
	DB *sql.DB
	// Platform GLKVM 平台客户端（UsbLink / 派车取件设备定位与 fileshare 转发；nil 时设备桥跳过）。
	Platform kvm.Platform
	// Locker 设备维护锁（派车取件结束解禁；可 nil）。
	Locker kvm.Locker
	// AdminUsername 管理员账号（设备代登缺省 / 派车系统建卡人缺省；env ADMIN_USERNAME）。
	AdminUsername string
}

// Wire 一行注入：装配 office 五源 + 网盘文档源、worklog/safeday 网盘保存、kvm 网盘互推、
// callme WF3 命名 + wechatanswer 群内提问/聊天归档直调与 PadPro 五通道、tree/crossing
// UsbLink 与结果通知、worklog 人工审核通知与派车单取件建卡（见各文件），并返回微信智能应答
// 捕获出口（赋给 wechatanswer.Module.Sink，同形 wechatanswer.CaptureSink）：
//
//	wam.Sink = bridge.Wire(bridge.Deps{Office: om, Worklog: wm, ...})
//
// 某模块为 nil 时跳过其桥；捕获出口在 tree/crossing 均缺席时返回 nil（wechatanswer 侧视为未接入）。
func Wire(d Deps) CaptureSink {
	if d.Office != nil {
		d.Office.Sources = OfficeSources(d.Worklog, d.Safeday, d.Tree, d.Crossing)
		if d.Netdisk != nil {
			// *netdisk.Module 的 ReadFileBytes / WriteFileBytes / DocKey 天然满足 office.NetdiskBridge，
			// 经 office.NewNetdiskDoc 适配为可编辑协同 + 保存写回的网盘文档源。
			d.Office.Netdisk = office.NewNetdiskDoc(d.Netdisk)
		}
	}
	if d.Netdisk != nil {
		if d.Worklog != nil {
			d.Worklog.Netdisk = NewWorklogSaver(d.Netdisk)
		}
		if d.Safeday != nil {
			d.Safeday.Netdisk = NewSafedaySaver(d.Netdisk)
		}
		if d.Kvm != nil {
			d.Kvm.Netdisk = NewKvmBridge(d.Netdisk)
		}
	}
	// WF3 文件命名（DIFY_WF3_API_KEY）+ 群内提问/聊天归档直调 + WeChatPadPro 五通道集中构造。
	if d.Callme != nil && d.Cfg != nil {
		d.Callme.Namer = NewWF3Namer(d.Cfg)
	}
	if d.Wechat != nil {
		if d.Callme != nil {
			d.Wechat.Ask = NewAskBridge(d.Callme, d.Cfg)
		}
		if d.Chatlog != nil {
			d.Wechat.Chatlog = NewChatlogBridge(d.Chatlog, d.Cfg)
		}
		WirePadPro(d.Wechat, NewPadPro(d.Cfg))
	}
	// 内网 U 盘同步设备链路（tree/crossing UsbLink，设备 ddns 各自配置键，未配置即停用）。
	if d.Platform != nil {
		if d.Tree != nil {
			d.Tree.Usb = NewUsbLink(d.Platform, d.Cfg, UsbKeys{
				DdnsKey: "TREEHAZARD_SYNC_DEVICE_DDNS",
				MacKey:  "TREEHAZARD_SYNC_DEVICE_MAC",
				UserKey: "TREEHAZARD_SYNC_KVM_USER",
			}, d.AdminUsername)
		}
		if d.Crossing != nil {
			d.Crossing.Usb = NewUsbLink(d.Platform, d.Cfg, UsbKeys{
				DdnsKey: "CROSSING_SYNC_DEVICE_DDNS",
				MacKey:  "CROSSING_SYNC_DEVICE_MAC",
				UserKey: "CROSSING_SYNC_KVM_USER",
			}, d.AdminUsername)
		}
	}
	// 站内通知 + 微信外发（tree/crossing 同步结果、worklog 人工审核）、派车单取件建卡。
	if d.DB != nil {
		if d.Tree != nil {
			d.Tree.SyncNotifier = NewRoleNotifier(d.DB, d.Cfg, "TREEHAZARD_SYNC_TEAM")
		}
		if d.Crossing != nil {
			d.Crossing.Notifier = NewRoleNotifier(d.DB, d.Cfg, "CROSSING_SYNC_TEAM")
		}
		if d.Worklog != nil {
			d.Worklog.Notifier = NewWorklogNotifier(d.DB, d.Cfg)
			if d.Platform != nil {
				d.Worklog.Dispatcher = NewDispatchSyncer(d.DB, d.Platform, d.Cfg, d.AdminUsername, d.Locker)
			}
		}
	}
	if d.Tree == nil && d.Crossing == nil {
		return nil
	}
	return NewCaptureSink(d.Tree, d.Crossing, d.Cfg)
}

// OfficeSources 装配 office 五源（wls/sdr/wlr/thd/crs）；nil 模块跳过对应源。
func OfficeSources(wl *worklog.Module, sd *safeday.Module, th *treehazard.Module, cr *crossing.Module) map[string]office.DocSource {
	var sheet office.SheetBuilder
	var remark office.RemarkFiles
	if wl != nil {
		sheet = worklog.NewSheetBuilder(wl)
		remark = worklog.NewRemarkFiles(wl)
	}
	var sdr office.SafedayFiles
	if sd != nil {
		sdr = safeday.NewSafedayFiles(sd)
	}
	var thd office.TeamExport
	if th != nil {
		thd = treehazard.NewTeamExport(th)
	}
	var crs office.TeamExport
	if cr != nil {
		crs = crossing.NewTeamExport(cr)
	}
	return office.NewSources(sheet, sdr, remark, thd, crs)
}
