package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"shade/internal/apps/usbcmd"
	"shade/internal/httpapi"
)

// HTTP 班组同步节点/节点表读取接口（挂在 /api/v1 下）。
type HTTP struct {
	Store *Store
	// Reader 旧单设备/默认 Hub 的 U 盘链路（tree/crossing UsbLink）；读取节点表用。
	Reader usbcmd.Link
}

// NewHTTP 构造 HTTP 模块。
func NewHTTP(store *Store) *HTTP {
	return &HTTP{Store: store}
}

type envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, env envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

func writeOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, envelope{Code: 0, Message: "ok", Data: data})
}

func fail(w http.ResponseWriter, status, code int, message string) {
	writeJSON(w, status, envelope{Code: code, Message: message})
}

// AppInfo 同步应用展示。
type AppInfo struct {
	App  string `json:"app"`
	Name string `json:"name"`
}

// BindingView 单个应用的绑定节点状态。
type BindingView struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Online   bool   `json:"online"`
	Ready    bool   `json:"ready"`
	Reason   string `json:"reason"`
}

// TeamSyncView 班组同步节点页数据。
type TeamSyncView struct {
	Team         httpapi.Team           `json:"team"`
	NightEnabled bool                   `json:"night_enabled"`
	Bindings     map[string]BindingView `json:"bindings"`
	Nodes        []Node                 `json:"nodes"`
	Apps         []AppInfo              `json:"apps"`
	UpdatedAt    string                 `json:"updated_at"`
	CacheEmpty   bool                   `json:"cache_empty"`
}

// RegisterRoutes 班组同步节点 + 节点表读取 + Hub 注册。
func (m *HTTP) RegisterRoutes(r chi.Router) {
	r.Get("/team-sync", m.get)
	r.Put("/team-sync", m.put)
	r.Put("/team-sync/night", m.night)
	r.Post("/team-sync/read-nodes", m.readNodes)
	r.Get("/team-sync/hubs", m.hubs)
	r.Put("/team-sync/hubs", m.hubPut)
}

func wantedTeamID(r *http.Request) int64 {
	raw := strings.TrimSpace(r.URL.Query().Get("team_id"))
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func (m *HTTP) enter(w http.ResponseWriter, r *http.Request) (httpapi.Team, int64, bool) {
	ident, ok := httpapi.IdentityFromContext(r.Context())
	if !ok {
		fail(w, http.StatusUnauthorized, 40101, "未登录或登录已过期")
		return httpapi.Team{}, 0, false
	}
	team, err := httpapi.ResolveTeam(r.Context(), m.Store.DB,
		httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}, wantedTeamID(r))
	if err != nil {
		fail(w, http.StatusBadRequest, 40010, "无可用班组: "+err.Error())
		return httpapi.Team{}, 0, false
	}
	if !httpapi.RequireTeamAdmin(
		httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}, team) {
		fail(w, http.StatusForbidden, 40301, "仅班管/超管可设置同步节点")
		return httpapi.Team{}, 0, false
	}
	return team, ident.ID, true
}

func (m *HTTP) enterAdmin(w http.ResponseWriter, r *http.Request) (int64, bool) {
	ident, ok := httpapi.IdentityFromContext(r.Context())
	if !ok {
		fail(w, http.StatusUnauthorized, 40101, "未登录或登录已过期")
		return 0, false
	}
	if ident.Role != "admin" {
		fail(w, http.StatusForbidden, 40302, "仅超管可执行此操作")
		return 0, false
	}
	return ident.ID, true
}

func (m *HTTP) view(ctx context.Context, team httpapi.Team) (TeamSyncView, error) {
	bindings, err := m.Store.ListBindings(ctx, team.ID)
	if err != nil {
		return TeamSyncView{}, err
	}
	nodes, err := m.Store.ListTeamNodes(ctx, team.ID)
	if err != nil {
		return TeamSyncView{}, err
	}
	night, err := m.Store.NightEnabled(ctx, team.ID)
	if err != nil {
		return TeamSyncView{}, err
	}
	updated, err := m.Store.CacheUpdatedAt(ctx)
	if err != nil {
		return TeamSyncView{}, err
	}
	views := map[string]BindingView{}
	for _, app := range []string{AppTreeSync, AppCrossingSync} {
		nodeID := bindings[app]
		if nodeID == "" {
			continue
		}
		view := BindingView{NodeID: nodeID, Reason: "节点不在缓存中"}
		for _, node := range nodes {
			if node.NodeID != nodeID {
				continue
			}
			view.NodeName = node.Name
			view.Online = node.Online
			ready, reason := node.Ready(app, team.ID)
			view.Ready = ready
			view.Reason = reason
			break
		}
		views[app] = view
	}
	return TeamSyncView{
		Team: team, NightEnabled: night, Bindings: views, Nodes: nodes,
		Apps: []AppInfo{
			{App: AppTreeSync, Name: AppNames[AppTreeSync]},
			{App: AppCrossingSync, Name: AppNames[AppCrossingSync]},
		},
		UpdatedAt: updated, CacheEmpty: len(nodes) == 0,
	}, nil
}

func (m *HTTP) get(w http.ResponseWriter, r *http.Request) {
	team, _, ok := m.enter(w, r)
	if !ok {
		return
	}
	view, err := m.view(r.Context(), team)
	if err != nil {
		fail(w, http.StatusInternalServerError, 50001, "读取同步节点失败: "+err.Error())
		return
	}
	writeOK(w, view)
}

type bindRequest struct {
	App    string `json:"app"`
	NodeID string `json:"node_id"`
}

func (m *HTTP) put(w http.ResponseWriter, r *http.Request) {
	team, userID, ok := m.enter(w, r)
	if !ok {
		return
	}
	var body bindRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, 40001, "请求格式错误")
		return
	}
	if err := m.Store.Bind(r.Context(), team.ID, body.App, body.NodeID, userID); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrNodeMissing) || errors.Is(err, ErrTeamMismatch) {
			status = http.StatusBadRequest
		}
		fail(w, status, 40002, "保存同步节点失败: "+err.Error())
		return
	}
	view, err := m.view(r.Context(), team)
	if err != nil {
		fail(w, http.StatusInternalServerError, 50001, "读取同步节点失败: "+err.Error())
		return
	}
	writeOK(w, view)
}

type nightRequest struct {
	Enabled bool `json:"enabled"`
}

func (m *HTTP) night(w http.ResponseWriter, r *http.Request) {
	team, userID, ok := m.enter(w, r)
	if !ok {
		return
	}
	var body nightRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, 40001, "请求格式错误")
		return
	}
	if err := m.Store.SetNightEnabled(r.Context(), team.ID, body.Enabled, userID); err != nil {
		fail(w, http.StatusInternalServerError, 50001, "保存夜间开关失败: "+err.Error())
		return
	}
	view, err := m.view(r.Context(), team)
	if err != nil {
		fail(w, http.StatusInternalServerError, 50001, "读取同步节点失败: "+err.Error())
		return
	}
	writeOK(w, view)
}

func (m *HTTP) readNodes(w http.ResponseWriter, r *http.Request) {
	_, ok := m.enterAdmin(w, r)
	if !ok {
		return
	}
	reader := m.Reader
	if reader == nil {
		fail(w, http.StatusServiceUnavailable, 50301, "U 盘链路未接入")
		return
	}
	ctx := r.Context()
	sessionKey := ""
	if hubID := strings.TrimSpace(r.URL.Query().Get("hub_id")); hubID != "" {
		hubs, err := m.Store.ListHubs(ctx)
		if err != nil {
			fail(w, http.StatusInternalServerError, 50001, "读取 Hub 列表失败: "+err.Error())
			return
		}
		found := false
		for _, hub := range hubs {
			if hub.ID == hubID {
				ctx = usbcmd.WithHub(ctx, hub.HubRef())
				sessionKey = hub.ID
				found = true
				break
			}
		}
		if !found {
			fail(w, http.StatusBadRequest, 40003, "Hub 不存在: "+hubID)
			return
		}
	}
	if err := usbcmd.DefaultSessions.Acquire(ctx, sessionKey); err != nil {
		fail(w, http.StatusLocked, 42301, "U 盘正在同步，请稍后再试")
		return
	}
	defer usbcmd.DefaultSessions.Release(sessionKey)
	defer usbcmd.RestoreShare(ctx, reader)
	// 先共享给被控机，Hub 的 usb-agent 挂载后会写盘根节点表；最多等 20s。
	if err := reader.MountToHost(ctx); err != nil {
		fail(w, http.StatusServiceUnavailable, 50303, "共享 U 盘失败: "+err.Error())
		return
	}
	var file NodesFile
	var err error
	deadline := time.Now().Add(20 * time.Second)
	for {
		file, err = m.Store.ReadNodes(ctx, reader)
		if err == nil || !errors.Is(err, ErrNodesFileMissing) {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		if errors.Is(err, ErrNodesFileMissing) {
			fail(w, http.StatusServiceUnavailable, 50302, "U 盘上没有节点表：Hub 可能未启用集群或尚未写入")
			return
		}
		fail(w, http.StatusInternalServerError, 50002, "读取节点表失败: "+err.Error())
		return
	}
	updated, _ := m.Store.CacheUpdatedAt(ctx)
	writeOK(w, map[string]any{
		"nodes":       len(file.Nodes),
		"updated_at":  updated,
		"cache_empty": len(file.Nodes) == 0,
	})
}

func (m *HTTP) hubs(w http.ResponseWriter, r *http.Request) {
	_, ok := m.enterAdmin(w, r)
	if !ok {
		return
	}
	hubs, err := m.Store.ListHubs(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, 50001, "读取 Hub 失败: "+err.Error())
		return
	}
	writeOK(w, map[string]any{"hubs": hubs})
}

func (m *HTTP) hubPut(w http.ResponseWriter, r *http.Request) {
	_, ok := m.enterAdmin(w, r)
	if !ok {
		return
	}
	var hub Hub
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&hub); err != nil {
		fail(w, http.StatusBadRequest, 40001, "请求格式错误")
		return
	}
	hub.ID = strings.TrimSpace(hub.ID)
	hub.Ddns = strings.TrimSpace(hub.Ddns)
	if hub.ID == "" || hub.Ddns == "" {
		fail(w, http.StatusBadRequest, 40004, "hub_id 与 ddns 必填")
		return
	}
	if err := m.Store.UpsertHub(r.Context(), hub); err != nil {
		fail(w, http.StatusInternalServerError, 50001, "保存 Hub 失败: "+err.Error())
		return
	}
	hubs, err := m.Store.ListHubs(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, 50001, "读取 Hub 失败: "+err.Error())
		return
	}
	writeOK(w, map[string]any{"hubs": hubs})
}

// 编译期确认 *sql.DB 依赖可注入；Store.DB 由 main 装配。
