// 文本与引用回执契约测试：原始 reply_context 透传，旧报文兼容补齐发送者。
package wechatanswer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func newSendGateway(t *testing.T, got *map[string]any) *PadProClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/Msg/Quote" {
			t.Errorf("请求路径不符：%s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			t.Errorf("请求 JSON 解析失败：%v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"Success": true, "Code": 0, "Data": map[string]any{}})
	}))
	t.Cleanup(srv.Close)
	return &PadProClient{Cfg: fakeCfg{values: map[string]string{
		"WECHATPADPRO_TOKEN": "test-token", "WECHATPADPRO_API_BASE": srv.URL,
	}}}
}

func TestSendQuotePassesOriginalReplyContext(t *testing.T) {
	var got map[string]any
	cli := newSendGateway(t, &got)
	reply := map[string]any{
		"chat_user_id": "room@chatroom", "conversation_id": "room@chatroom",
		"from_user_id": "member-wxid", "msg_id": float64(7), "msg_type": float64(1),
		"new_msg_id": "9007199254740993", "quote_content": "原始报送", "sequence": float64(123),
		"svr_id": "9007199254740993", "to_wxid": "room@chatroom",
	}
	if err := cli.SendQuote(context.Background(), "room@chatroom", "已记录", QuoteRef{ReplyContext: reply}); err != nil {
		t.Fatalf("SendQuote：%v", err)
	}
	if got["content"] != "已记录" || !reflect.DeepEqual(got["reply_context"], reply) {
		t.Fatalf("原始 reply_context 未原样透传：%+v", got)
	}
}

func TestSendQuoteFallbackFillsSender(t *testing.T) {
	var got map[string]any
	cli := newSendGateway(t, &got)
	err := cli.SendQuote(context.Background(), "room@chatroom", "已记录", QuoteRef{
		SvrID: "svr-1", Content: "原始报送", CreateTime: "123", FromUserID: "member-wxid",
	})
	if err != nil {
		t.Fatalf("SendQuote：%v", err)
	}
	reply, _ := got["reply_context"].(map[string]any)
	if got["from_user_id"] != "member-wxid" || reply["from_user_id"] != "member-wxid" {
		t.Fatalf("旧报文回退未补齐发送者：%+v", got)
	}
}

type quoteContextCapture struct{ got QuoteRef }

func (s *quoteContextCapture) SendText(context.Context, string, string) error { return nil }
func (s *quoteContextCapture) SendQuote(_ context.Context, _ string, _ string, quote QuoteRef) error {
	s.got = quote
	return nil
}

func TestReceiptQuoteCarriesIncomingReplyContext(t *testing.T) {
	sender := &quoteContextCapture{}
	m := &Module{Sender: sender}
	reply := map[string]any{"from_user_id": "member-wxid", "chat_user_id": testGroup, "svr_id": "svr-1"}
	msg := &Message{
		ConversationID: testGroup, SenderID: "member-wxid", SvrID: "svr-1", Content: "报送", CreatedAt: 123,
		Raw: map[string]any{"reply_context": reply},
	}
	m.receiptQuote(context.Background(), msg, "已记录")
	if !reflect.DeepEqual(sender.got.ReplyContext, reply) || sender.got.FromUserID != "member-wxid" {
		t.Fatalf("回执未携带入站上下文：%+v", sender.got)
	}
}
