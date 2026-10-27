package authwx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WXSession 微信 code2session 会话。openid / unionid / session_key 仅服务内部使用，
// 绝不写入响应、日志或用户可见结构（小程序只拿到 wx_bound 绑定状态）。
type WXSession struct {
	OpenID     string
	UnionID    string
	SessionKey string
}

// Code2Sessioner 微信 code2session 适配（可注入）；测试注入假实现，绝不连微信。
type Code2Sessioner interface {
	Code2Session(ctx context.Context, appID, secret, code string) (WXSession, error)
}

// HTTPCode2Session 生产 HTTP 适配：GET https://api.weixin.qq.com/sns/jscode2session
// （appid / secret / js_code / grant_type=authorization_code，10s 超时，同 Node axios 调用）。
// Client 与 BaseURL 均可注入：测试指向 httptest.Server，不触达微信服务器。
type HTTPCode2Session struct {
	Client  *http.Client // nil → 10s 超时默认客户端
	BaseURL string       // 空 → 官方地址
}

// code2sessionURL 官方换会话地址。
const code2sessionURL = "https://api.weixin.qq.com/sns/jscode2session"

func (h *HTTPCode2Session) Code2Session(ctx context.Context, appID, secret, code string) (WXSession, error) {
	if appID == "" || secret == "" {
		return WXSession{}, errors.New("微信登录未配置（WX_APPID / WX_SECRET）")
	}
	base := strings.TrimRight(h.BaseURL, "?")
	if base == "" {
		base = code2sessionURL
	}
	query := url.Values{}
	query.Set("appid", appID)
	query.Set("secret", secret)
	query.Set("js_code", code)
	query.Set("grant_type", "authorization_code")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+query.Encode(), nil)
	if err != nil {
		return WXSession{}, err
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return WXSession{}, err
	}
	defer resp.Body.Close()
	var payload struct {
		OpenID     string `json:"openid"`
		UnionID    string `json:"unionid"`
		SessionKey string `json:"session_key"`
		ErrCode    int    `json:"errcode"`
		ErrMsg     string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return WXSession{}, err
	}
	if payload.ErrCode != 0 {
		msg := payload.ErrMsg
		if msg == "" {
			msg = fmt.Sprintf("微信接口错误（%d）", payload.ErrCode)
		}
		return WXSession{}, errors.New(msg)
	}
	if payload.OpenID == "" {
		return WXSession{}, errors.New("微信接口返回缺少 openid")
	}
	return WXSession{OpenID: payload.OpenID, UnionID: payload.UnionID, SessionKey: payload.SessionKey}, nil
}
