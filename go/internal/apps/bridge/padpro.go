// WeChatPadPro 客户端集中构造：一个 PadProClient 同时满足 SyncSource / MessageSender /
// ImageDownloader / FileDownloader（+FileSender）/ MemberSource 五通道，本文件为唯一构造入口，
// 避免 main/各处散装拼装；连接配置（WECHATPADPRO_*）由模块每次调用热读取，
// Token 未配置时不建立任何网络连接（wechatanswer 模块内自行守卫）。
package bridge

import "shade/internal/apps/wechatanswer"

// NewPadPro 集中构造 WeChatPadPro 云端网关客户端（HTTP 超时/WS 拨号可经字段注入测试桩）。
func NewPadPro(cfg wechatanswer.ConfigGet) *wechatanswer.PadProClient {
	return &wechatanswer.PadProClient{Cfg: cfg}
}

// WirePadPro 把 PadProClient 接到 wechatanswer.Module 的消息/媒体/成员五通道字段。
// p 为 nil 时清空五通道（视为未接入，模块内对应能力禁用）。
func WirePadPro(wa *wechatanswer.Module, p *wechatanswer.PadProClient) {
	if wa == nil {
		return
	}
	if p == nil {
		wa.Sync, wa.Sender, wa.Images, wa.Files, wa.Members = nil, nil, nil, nil, nil
		return
	}
	wa.Sync = p
	wa.Sender = p
	wa.Images = p
	wa.Files = p
	wa.Members = p
}
