package sgccclockin

// 通知实现测试：站内/微信发送函数注入记录，收件人与 wxid 从 fake 库解析；
// 文案与收件口径对照 server/src/sgccclockin/index.js 的 notify*（提醒仅微信、群消息合并 @）。

import (
	"context"
	"database/sql/driver"
	"strings"
	"sync"
	"testing"
)

type inAppCall struct {
	userIDs []int64
	roles   []string
	title   string
	content string
}

type wxCall struct {
	target string
	text   string
}

// fakeSend 记录站内/微信发送（注入 PushNotifier）。
type fakeSend struct {
	mu    sync.Mutex
	inApp []inAppCall
	wx    []wxCall
}

func (s *fakeSend) inAppSend(_ context.Context, userIDs []int64, roles []string, title, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inApp = append(s.inApp, inAppCall{userIDs: userIDs, roles: roles, title: title, content: content})
	return nil
}

func (s *fakeSend) wxSend(_ context.Context, target, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wx = append(s.wx, wxCall{target: target, text: text})
	return nil
}

// noticeCtx 通用库桩：超管 + 用户 wxid/昵称 + 班组群 wxid。
func noticeCtx(fdb *fakeDB) {
	fdb.onRowsAlways("FROM sys_user WHERE role = 'admin'", []string{"id"},
		[]driver.Value{int64(99)})
	fdb.onRowsAlways("FROM sys_user WHERE role = 'team_admin'", []string{"id"},
		[]driver.Value{int64(50)})
	fdb.onRowsAlways("SELECT id, wxid, nickname FROM sys_user", []string{"id", "wxid", "nickname"},
		[]driver.Value{int64(7), "wx_7", "张三"},
		[]driver.Value{int64(8), "wx_8", "李四"},
		[]driver.Value{int64(99), "wx_99", "超管"})
	fdb.onRowsAlways("SELECT wxid FROM sys_team", []string{"wxid"}, []driver.Value{"wxroom_1"})
}

// TestNotifyTokenExpired 登录过期：站内+微信投放本人与超管，班组群一条并@本人。
func TestNotifyTokenExpired(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	fdb.onRowsAlways("SELECT name, user_id FROM worklog_member", []string{"name", "user_id"},
		[]driver.Value{"张三", int64(7)})
	fdb.onRowsAlways("SELECT team_id FROM sys_user", []string{"team_id"}, []driver.Value{int64(1)})
	noticeCtx(fdb)
	send := &fakeSend{}
	p := &PushNotifier{DB: db, InApp: send.inAppSend, Wx: send.wxSend}

	if err := p.NotifyTokenExpired(context.Background(), Account{UserID: 7, MemberID: 11}); err != nil {
		t.Fatalf("通知应成功: %v", err)
	}
	if len(send.inApp) != 1 {
		t.Fatalf("站内应一条: %d", len(send.inApp))
	}
	call := send.inApp[0]
	if call.title != "商旅登录已过期" || !strings.Contains(call.content, "张三") {
		t.Fatalf("站内内容不符: %+v", call)
	}
	if !containsID(call.userIDs, 7) || !containsID(call.userIDs, 99) {
		t.Fatalf("收件人应含本人与超管: %v", call.userIDs)
	}
	// 微信：个人 + 班组群（@前缀）
	var personal, group bool
	for _, c := range send.wx {
		if c.target == "wx_7" && strings.Contains(c.text, "登录已过期") {
			personal = true
		}
		if c.target == "wxroom_1" && strings.HasPrefix(c.text, "@张三 ") {
			group = true
		}
	}
	if !personal || !group {
		t.Fatalf("微信个人/群口径不符: %+v", send.wx)
	}
}

// TestNotifyRemindEndWxOnly 结束打卡提醒仅微信：个人按人 + 班组群合并一条并@缺卡成员。
func TestNotifyRemindEndWxOnly(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	noticeCtx(fdb)
	send := &fakeSend{}
	p := &PushNotifier{DB: db, InApp: send.inAppSend, Wx: send.wxSend}

	err := p.NotifyRemind(context.Background(), "end", []MissingClockinRow{
		{TeamID: 1, MemberID: 11, Name: "张三", UserID: 7, StartHM: "08:30"},
		{TeamID: 1, MemberID: 12, Name: "李四", UserID: 8},
	})
	if err != nil {
		t.Fatalf("提醒应成功: %v", err)
	}
	if len(send.inApp) != 0 {
		t.Fatalf("提醒不写站内: %+v", send.inApp)
	}
	var group *wxCall
	personals := 0
	for i := range send.wx {
		c := &send.wx[i]
		switch {
		case c.target == "wx_7" && strings.Contains(c.text, "结束打卡提醒"):
			personals++
		case c.target == "wx_8" && strings.Contains(c.text, "结束打卡提醒"):
			personals++
		case c.target == "wxroom_1":
			group = c
		}
	}
	if personals != 2 {
		t.Fatalf("应按人两条个人微信: %+v", send.wx)
	}
	if group == nil || !strings.Contains(group.text, "李四") || !strings.Contains(group.text, "08:30 已打开始卡") {
		t.Fatalf("群消息内容不符: %+v", group)
	}
	if !strings.HasPrefix(group.text, "@") || !strings.Contains(group.text, "@张三 ") || !strings.Contains(group.text, "@李四 ") {
		t.Fatalf("群消息应@缺卡成员: %q", group.text)
	}
}

// TestNotifyRemindStart 开始打卡提醒文案。
func TestNotifyRemindStart(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	noticeCtx(fdb)
	send := &fakeSend{}
	p := &PushNotifier{DB: db, InApp: send.inAppSend, Wx: send.wxSend}

	if err := p.NotifyRemind(context.Background(), "start", []MissingClockinRow{
		{TeamID: 1, MemberID: 11, Name: "张三", UserID: 7},
	}); err != nil {
		t.Fatalf("提醒应成功: %v", err)
	}
	found := false
	for _, c := range send.wx {
		if c.target == "wx_7" && strings.Contains(c.text, "尚未打开始卡（首次打卡）") {
			found = true
		}
	}
	if !found {
		t.Fatalf("开始卡提醒文案不符: %+v", send.wx)
	}
}

// TestNotifyDailySyncResult 核查日报仅超管：汇总 + 失败明细。
func TestNotifyDailySyncResult(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	noticeCtx(fdb)
	send := &fakeSend{}
	p := &PushNotifier{DB: db, InApp: send.inAppSend, Wx: send.wxSend}

	err := p.NotifyDailySyncResult(context.Background(), 1, "2026-10-27", SyncSummary{
		TeamName: "检修一班", OK: 3, Diff: 1, Fail: 1,
		Fails: []SyncLogRow{{Type: "photo", Detail: "成员「李四」商旅登录已过期", MemberID: 12, MemberName: "李四"}},
	})
	if err != nil {
		t.Fatalf("日报应成功: %v", err)
	}
	if len(send.inApp) != 1 || send.inApp[0].title != "商旅每日核查完成（检修一班）" {
		t.Fatalf("站内日报不符: %+v", send.inApp)
	}
	content := send.inApp[0].content
	if !strings.Contains(content, "一致 3") || !strings.Contains(content, "李四｜照片") {
		t.Fatalf("日报内容不符: %s", content)
	}
	if !containsID(send.inApp[0].userIDs, 99) {
		t.Fatalf("日报仅发超管: %v", send.inApp[0].userIDs)
	}
}

// TestNotifyProxyState 代理异常：脱敏地址进正文，仅超管。
func TestNotifyProxyState(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	noticeCtx(fdb)
	send := &fakeSend{}
	p := &PushNotifier{
		DB: db, InApp: send.inAppSend, Wx: send.wxSend,
		ProxyURL: func(context.Context) string { return "socks5://user:pass@127.0.0.1:1080" },
	}
	if err := p.NotifyProxyState(context.Background(), "连接超时", false); err != nil {
		t.Fatalf("通知应成功: %v", err)
	}
	if len(send.inApp) != 1 || send.inApp[0].title != "商旅出口代理异常" {
		t.Fatalf("站内不符: %+v", send.inApp)
	}
	if !strings.Contains(send.inApp[0].content, "//***@127.0.0.1:1080") {
		t.Fatalf("代理地址应脱敏: %s", send.inApp[0].content)
	}
	if strings.Contains(send.inApp[0].content, "user:pass") {
		t.Fatalf("不得泄露代理凭据: %s", send.inApp[0].content)
	}
	// 恢复
	send.inApp = nil
	if err := p.NotifyProxyState(context.Background(), "", true); err != nil {
		t.Fatalf("通知应成功: %v", err)
	}
	if send.inApp[0].title != "商旅出口代理已恢复" {
		t.Fatalf("恢复文案不符: %+v", send.inApp[0])
	}
}

// TestNotifyManualCheck 人工核查：超管 + 涉及班管，行名缺省回查成员。
func TestNotifyManualCheck(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	noticeCtx(fdb)
	fdb.onRowsAlways("SELECT name FROM worklog_member WHERE id = ?", []string{"name"}, []driver.Value{"李四"})
	send := &fakeSend{}
	p := &PushNotifier{DB: db, InApp: send.inAppSend, Wx: send.wxSend}

	err := p.NotifyManualCheck(context.Background(), "午间提醒", []MissingClockinRow{
		{TeamID: 1, MemberID: 12},
	}, "午间提醒提醒前商旅同步失败（打卡状态未知），本次已跳过其自动打卡核查")
	if err != nil {
		t.Fatalf("通知应成功: %v", err)
	}
	if len(send.inApp) != 1 || send.inApp[0].title != "打卡核查需人工确认" {
		t.Fatalf("站内不符: %+v", send.inApp)
	}
	if !strings.Contains(send.inApp[0].content, "李四") {
		t.Fatalf("应回查成员名: %s", send.inApp[0].content)
	}
	if !containsID(send.inApp[0].userIDs, 99) || !containsID(send.inApp[0].userIDs, 50) {
		t.Fatalf("收件应含超管与班管: %v", send.inApp[0].userIDs)
	}
}

// TestPushNotifierNilChannels 渠道未注入时静默跳过（不 panic、不报错）。
func TestPushNotifierNilChannels(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	noticeCtx(fdb)
	p := &PushNotifier{DB: db}
	if err := p.NotifyTokenExpired(context.Background(), Account{UserID: 7, MemberID: 11}); err != nil {
		t.Fatalf("渠道缺失不应报错: %v", err)
	}
}

// TestWxSendFromSender SendText 适配（main 接 wechatanswer.Sender 同签名）。
func TestWxSendFromSender(t *testing.T) {
	var gotTarget, gotText string
	s := TextSender(senderFunc(func(_ context.Context, target, text string) error {
		gotTarget, gotText = target, text
		return nil
	}))
	if err := WxSendFrom(s)(context.Background(), "wxroom_1", "你好"); err != nil {
		t.Fatalf("发送应成功: %v", err)
	}
	if gotTarget != "wxroom_1" || gotText != "你好" {
		t.Fatalf("适配不符: %q %q", gotTarget, gotText)
	}
}

type senderFunc func(ctx context.Context, target, text string) error

func (f senderFunc) SendText(ctx context.Context, target, text string) error {
	return f(ctx, target, text)
}

func containsID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
