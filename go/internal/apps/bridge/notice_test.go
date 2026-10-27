package bridge

// 通知适配测试：RoleNotifier / WorklogNotifier（fake）与 DifyWxPusher（httptest）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeNotice NoticePusher 桩。
type fakeNotice struct {
	gotTargets []string
	gotUsers   []int64
	gotTitle   string
	gotContent string
	err        error
}

func (f *fakeNotice) PushNotice(_ context.Context, targets []string, userIds []int64, title, content string) error {
	f.gotTargets, f.gotUsers, f.gotTitle, f.gotContent = targets, userIds, title, content
	return f.err
}

// fakeWx WxPusher 桩。
type fakeWx struct {
	calls int
	text  string
	teams []int64
	ats   []int64
}

func (f *fakeWx) Send(_ context.Context, _, teamIds []int64, text string, atUserIds []int64) (int, error) {
	f.calls++
	f.text, f.teams, f.ats = text, teamIds, atUserIds
	return 1, nil
}

// fakeReview ReviewSource 桩。
type fakeReview struct {
	info  *ReviewInfo
	admin []int64
	err   error
}

func (f *fakeReview) PhotoReviewInfo(context.Context, int64) (*ReviewInfo, error) {
	return f.info, f.err
}

func (f *fakeReview) TeamAdminIDs(context.Context, int64) ([]int64, error) {
	return f.admin, nil
}

func TestRoleNotifierPush(t *testing.T) {
	n := &fakeNotice{}
	w := &fakeWx{}
	r := &RoleNotifier{Notice: n, Wx: w, WxTeams: func(context.Context) []int64 { return []int64{42} }}
	if err := r.Push(context.Background(), []string{"admin", "team_admin"}, "标题", "正文"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(n.gotTargets) != 2 || n.gotTitle != "标题" || n.gotContent != "正文" {
		t.Fatalf("notice = %+v", n)
	}
	if w.calls != 1 || len(w.teams) != 1 || w.teams[0] != 42 {
		t.Fatalf("wx = %+v", w)
	}
	if !strings.Contains(w.text, "标题\n正文") {
		t.Fatalf("wx text = %q", w.text)
	}
}

func TestRoleNotifierNoWxTeam(t *testing.T) {
	n := &fakeNotice{}
	r := &RoleNotifier{Notice: n}
	if err := r.Push(context.Background(), []string{"admin"}, "t", "c"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if n.gotTitle != "t" {
		t.Fatalf("notice = %+v", n)
	}
}

func TestWorklogNotifierManualReview(t *testing.T) {
	n := &fakeNotice{}
	w := &fakeWx{}
	r := &WorklogNotifier{
		Src:    &fakeReview{info: &ReviewInfo{TeamID: 8, LogDate: "2026-10-27", PlateNo: "晋J12345", Members: []string{"张三", "李四"}}, admin: []int64{2, 3}},
		Notice: n,
		Wx:     w,
	}
	if err := r.NotifyManualReview(context.Background(), 99); err != nil {
		t.Fatalf("NotifyManualReview: %v", err)
	}
	if len(n.gotUsers) != 2 || n.gotTitle != "水印照片需人工审核" {
		t.Fatalf("notice = %+v", n)
	}
	for _, want := range []string{"2026-10-27", "晋J12345", "张三、李四", "3 次重试", "验证失败"} {
		if !strings.Contains(n.gotContent, want) {
			t.Fatalf("content missing %q: %s", want, n.gotContent)
		}
	}
	if w.calls != 1 || w.teams[0] != 8 || len(w.ats) != 2 {
		t.Fatalf("wx = %+v", w)
	}

	// 照片已删除 → 静默无通知。
	n2 := &fakeNotice{}
	r2 := &WorklogNotifier{Src: &fakeReview{info: nil}, Notice: n2}
	if err := r2.NotifyManualReview(context.Background(), 1); err != nil {
		t.Fatalf("deleted photo: %v", err)
	}
	if n2.gotTitle != "" {
		t.Fatalf("unexpected notice: %+v", n2)
	}
}

func TestWorklogNotifierNoPlate(t *testing.T) {
	n := &fakeNotice{}
	r := &WorklogNotifier{
		Src:    &fakeReview{info: &ReviewInfo{TeamID: 8, LogDate: "2026-10-27"}, admin: []int64{2}},
		Notice: n,
	}
	if err := r.NotifyManualReview(context.Background(), 5); err != nil {
		t.Fatalf("NotifyManualReview: %v", err)
	}
	if !strings.Contains(n.gotContent, "未出车") {
		t.Fatalf("content = %s", n.gotContent)
	}
}

// fakeResolver WxidResolver 桩。
type fakeResolver struct {
	wxids    []string
	mentions []Mention
}

func (f *fakeResolver) Wxids(context.Context, []int64, []int64) ([]string, error) {
	return f.wxids, nil
}

func (f *fakeResolver) Mentions(context.Context, []int64) ([]Mention, error) {
	return f.mentions, nil
}

func TestDifyWxPusherSend(t *testing.T) {
	type call struct {
		Wxid string `json:"wxid"`
		Text string `json:"text"`
		At   string `json:"at"`
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workflows/run" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body struct {
			Inputs map[string]string `json:"inputs"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, call{Wxid: body.Inputs["wxid"], Text: body.Inputs["text"], At: body.Inputs["at"]})
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"outputs": map[string]any{}}})
	}))
	defer srv.Close()
	cfg := fakeCfg{"DIFY_API_URL": srv.URL, "DIFY_WXPUSH_API_KEY": "wx-key"}
	p := NewDifyWxPusher(cfg, &fakeResolver{
		wxids:    []string{"u1", "g1@chatroom"},
		mentions: []Mention{{Wxid: "m1", Nickname: "张三"}},
	})
	sent, err := p.Send(context.Background(), nil, []int64{8}, "标题\n正文", []int64{2})
	if err != nil || sent != 2 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].At != "" || calls[0].Text != "标题\n正文" {
		t.Fatalf("personal call = %+v", calls[0])
	}
	if calls[1].At != "m1" || !strings.HasPrefix(calls[1].Text, "@张三 \n") {
		t.Fatalf("room call = %+v", calls[1])
	}
}

func TestDifyWxPusherNoReceiver(t *testing.T) {
	p := NewDifyWxPusher(fakeCfg{"DIFY_API_URL": "http://127.0.0.1:0", "DIFY_WXPUSH_API_KEY": "k"},
		&fakeResolver{wxids: nil})
	if _, err := p.Send(context.Background(), []int64{1}, nil, "t", nil); err == nil {
		t.Fatal("expect error")
	}
}
