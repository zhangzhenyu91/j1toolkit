package httpapi

import (
	"net/http"
	"testing"

	"shade/internal/auth"
)

// 生产门控回归锁定：微信智能应答仍仅超管，班组管理员即便有应用授权也不得进入。
func TestWechatAnswerRemainsAdminOnly(t *testing.T) {
	echo := &echoModule{method: http.MethodGet, pattern: "/wechatanswer/groups"}
	h := NewRouter(Deps{Auth: &fakeAuth{ident: auth.Identity{
		ID: 9, Username: "tm", Role: "team_admin", TeamID: 1,
	}}}, echo)
	status, r := doReqAuth(t, h, http.MethodGet, "/api/v1/wechat-answer/groups", "")
	assertErr(t, status, http.StatusForbidden, r, "forbidden")
	if echo.hit {
		t.Fatal("班组管理员不得进入微信智能应答管理接口")
	}

	echoAdmin := &echoModule{method: http.MethodGet, pattern: "/wechatanswer/groups"}
	h = NewRouter(Deps{Auth: &fakeAuth{ident: auth.Identity{
		ID: 1, Username: "admin", Role: "admin", TeamID: 1,
	}}}, echoAdmin)
	status, r = doReqAuth(t, h, http.MethodGet, "/api/v1/wechat-answer/groups", "")
	if status != http.StatusOK || !echoAdmin.hit {
		t.Fatalf("超管应可进入，status=%d body=%+v hit=%v", status, r, echoAdmin.hit)
	}
}
