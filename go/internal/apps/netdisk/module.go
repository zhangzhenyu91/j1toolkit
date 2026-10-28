package netdisk

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// ConfigReader 运行时配置读取（*config.Store 满足；测试可注入桩）。
type ConfigReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// AppPermit 应用权限检查（对应 Node requireApp('netdisk')）；nil 时放行。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// AllowAllPermit 放行全部用户（本地开发/测试用）。
type AllowAllPermit struct{}

func (AllowAllPermit) Allow(context.Context, int64, string) (bool, error) { return true, nil }

// Thumbnailer 缩略图生成（≤384px webp）；nil 时 /thumb 返回 503（同 Node 缺 sharp 口径）。
type Thumbnailer interface {
	MakeThumb(src []byte) (webp []byte, err error)
}

// Module 团队网盘模块。
type Module struct {
	DB *sql.DB
	// OL OpenList 客户端（可注入）；nil 报依赖未接入。
	OL OpenListClient
	// Base 空间根静态配置（Cfg 缺值时回退；测试/旧装配口径）。
	Base BaseDirs
	// Cfg 运行时配置（NETDISK_PUBLIC_ROOT / NETDISK_PERSONAL_ROOT 空间根热更新）；nil 只用 Base。
	Cfg ConfigReader
	// JWTSecret 分享/预览票据签名密钥（JWT_SECRET）。
	JWTSecret string
	// MaxUploadMB 单文件上传上限（NETDISK_MAX_UPLOAD_MB，默认 2048）。
	MaxUploadMB int64
	// ThumbMaxMB 缩略图原图上限（NETDISK_THUMB_MAX_MB，默认 20）。
	ThumbMaxMB int64
	// ThumbDir 缩略图缓存目录（NETDISK_THUMB_DIR）。
	ThumbDir string
	// Thumb 缩略图生成器；nil 返回 503。
	Thumb Thumbnailer
	// Permit 应用权限检查；nil 放行。
	Permit AppPermit
	// Auth 可选认证器：/preview/sign 等被 publicAuthPath 前缀放行的登录态路由，
	// 由 withContext 回落自行解析 Bearer（nil 时仅依赖 AuthMiddleware 注入）。
	Auth httpapi.Authenticator

	thumbSem chan struct{}
}

// New 构造模块（db 必填）。
func New(db *sql.DB) *Module {
	return &Module{
		DB:       db,
		Permit:   AllowAllPermit{},
		thumbSem: make(chan struct{}, 2), // 生成并发闸 2（同 Node）
	}
}

// EnsureSchema 建表（幂等）。
func (m *Module) EnsureSchema(ctx context.Context) error {
	_, err := m.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS nd_share (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    share_id VARCHAR(32) NOT NULL COMMENT '分享标识（外链 /share.html#/s/<share_id>）',
    user_id BIGINT UNSIGNED NOT NULL COMMENT '创建人，关联 sys_user.id',
    creator VARCHAR(64) NOT NULL DEFAULT '' COMMENT '创建人昵称（冗余，免联表）',
    base_path VARCHAR(500) NOT NULL COMMENT '空间根（OpenList 绝对路径，越界校验基准）',
    paths JSON NOT NULL COMMENT '分享项（相对 base_path 的路径数组）',
    password VARCHAR(16) NOT NULL COMMENT '提取码',
    expire_at DATETIME NULL COMMENT '过期时间，NULL=永久',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 生效 0 停用',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_share_id (share_id),
    KEY idx_user (user_id, status),
    KEY idx_status (status, expire_at)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='团队网盘分享链接'`)
	return err
}

// RegisterRoutes 注册 /api/v1/netdisk 路由。
// 公开分享 /public/share/* 与预览回源 /preview/:token 为免登录路由（主程序 publicAuthPath
// 已放行 /api/v1/netdisk/preview；/api/v1/netdisk/public 需同样放行）。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/netdisk", func(r chi.Router) {
		// ---- 免登录区 ----
		r.Get("/preview/{token}", m.handlePreviewStream)
		r.Get("/preview/{token}/{name}", m.handlePreviewStream)
		// 公开分享（Express 非严格路由：带/不带尾斜杠同径，两形态都注册）。
		r.Get("/public/share/{id}", m.handleShareInfo)
		r.Get("/public/share/{id}/", m.handleShareInfo)
		r.Post("/public/share/{id}/verify", m.handleShareVerify)
		r.Post("/public/share/{id}/list", m.handleShareList)
		r.Get("/public/share/{id}/download", m.handleShareDownload)

		// ---- 登录 + netdisk 应用权限 ----
		r.Group(func(r chi.Router) {
			r.Use(m.withContext)
			r.Get("/spaces", m.handleSpaces)
			r.Post("/list", m.handleList)
			r.Post("/search", m.handleSearch)
			r.Get("/dirsize", m.handleDirSize)
			r.Post("/mkdir", m.handleMkdir)
			r.Post("/rename", m.handleRename)
			r.Post("/rename/batch", m.handleRenameBatch)
			r.Post("/remove", m.handleRemove)
			r.Post("/move", m.handleMove)
			r.Post("/copy", m.handleCopy)
			r.Post("/archive/meta", m.handleArchiveMeta)
			r.Post("/archive/list", m.handleArchiveList)
			r.Post("/archive/decompress", m.handleArchiveDecompress)
			r.Get("/archive/download", m.handleArchiveDownload)
			r.Get("/download/dir", m.handleDownloadDir)
			r.Get("/download", m.handleDownload)
			r.Get("/thumb", m.handleThumb)
			r.Post("/preview/sign", m.handlePreviewSign)
			r.Post("/upload/init", m.handleUploadInit)
			r.Put("/upload/chunk", m.handleUploadChunk)
			r.Post("/upload/complete", m.handleUploadComplete)
			r.Get("/upload/status", m.handleUploadStatus)
			r.Post("/upload/abort", m.handleUploadAbort)
			r.Get("/shares", m.handleSharesList)
			r.Post("/shares", m.handleShareCreate)
			r.Post("/shares/{id}/disable", m.handleShareDisable)
			r.Post("/shares/{id}/enable", m.handleShareEnable)
			r.Delete("/shares/{id}", m.handleShareDelete)
			r.Post("/save", m.handleSaveToNetdisk)
		})
	})
}

type moduleContextKey string

const (
	teamContextKey     moduleContextKey = "netdisk.team"
	identityContextKey moduleContextKey = "netdisk.identity"
)

func teamFrom(ctx context.Context) (httpapi.Team, bool) {
	t, ok := ctx.Value(teamContextKey).(httpapi.Team)
	return t, ok
}

func identityFrom(ctx context.Context) (auth.Identity, bool) {
	v, ok := ctx.Value(identityContextKey).(auth.Identity)
	return v, ok
}

// withContext 中间件：身份 → netdisk 应用权限 → 生效班组（超管可用 ?team_id= 指定）。
// 身份优先取 AuthMiddleware 注入；缺失时（/preview/sign 落在免登录前缀内）回落解析 Bearer。
func (m *Module) withContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ident, ok := httpapi.IdentityFromContext(r.Context())
		if !ok && m.Auth != nil {
			token := bearerToken(r)
			var err error
			ident, err = m.Auth.Authenticate(r.Context(), token)
			if err != nil {
				writeFail(w, http.StatusUnauthorized, codeBadTicket, "未登录或登录已过期", nil)
				return
			}
			ok = true
		}
		if !ok {
			writeFail(w, http.StatusUnauthorized, codeBadTicket, "未登录或登录已过期", nil)
			return
		}
		if m.Permit != nil {
			allowed, err := m.Permit.Allow(r.Context(), ident.ID, "netdisk")
			if err != nil {
				writeFail(w, http.StatusInternalServerError, codeInternal, "应用权限校验失败", nil)
				return
			}
			if !allowed {
				writeFail(w, http.StatusForbidden, codeAppDenied, "暂无该应用的使用权限，请联系管理员开通", nil)
				return
			}
		}
		team, err := httpapi.ResolveTeam(r.Context(), m.DB,
			httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}, wantedTeamID(r))
		if err != nil && !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "未分配班组") {
			writeFail(w, http.StatusInternalServerError, codeInternal, "班组解析失败", nil)
			return
		}
		ctx := context.WithValue(r.Context(), identityContextKey, ident)
		if err == nil {
			ctx = context.WithValue(ctx, teamContextKey, team)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func wantedTeamID(r *http.Request) int64 {
	if v := strings.TrimSpace(r.URL.Query().Get("team_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// bearerToken 解析 Authorization: Bearer <token>。
func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) >= 7 && strings.EqualFold(value[:7], "Bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

// teamNameOf 班组名（公共区根）。
func (m *Module) teamNameOf(ctx context.Context, ident auth.Identity) string {
	if ident.TeamID == 0 {
		return ""
	}
	var name string
	if err := m.DB.QueryRowContext(ctx, `SELECT name FROM sys_team WHERE id = ?`, ident.TeamID).Scan(&name); err != nil {
		return ""
	}
	return name
}

// baseDirs 生效空间根：运行时配置非空优先（热更新），回退静态 Base（测试/旧装配口径）。
func (m *Module) baseDirs(ctx context.Context) BaseDirs {
	d := m.Base
	if m.Cfg == nil {
		return d
	}
	if v, err := m.Cfg.Get(ctx, "NETDISK_PERSONAL_ROOT"); err == nil && strings.TrimSpace(v) != "" {
		d.PersonalRoot = strings.TrimSpace(v)
	}
	if v, err := m.Cfg.Get(ctx, "NETDISK_PUBLIC_ROOT"); err == nil && strings.TrimSpace(v) != "" {
		d.PublicRoot = strings.TrimSpace(v)
	}
	return d
}

// resolveRoot 空间根解析：字符串=可用根；已响应错误返回 ok=false。
func (m *Module) resolveRoot(w http.ResponseWriter, r *http.Request, space string) (string, bool) {
	ident, _ := identityFrom(r.Context())
	teamName := m.teamNameOf(r.Context(), ident)
	root, ok := SpaceRootOf(m.baseDirs(r.Context()), ident.Username, teamName, space)
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：space", nil)
		return "", false
	}
	if root == "" {
		writeFail(w, http.StatusBadRequest, codeNoTeam, "未分配班组，无公共区", nil)
		return "", false
	}
	return root, true
}

// mapItemSrc OpenList 文件对象原始形状。
type mapItemSrc struct {
	Name     string `json:"name"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

// mapItem OpenList 文件对象 → 前端条目。
func mapItem(o mapItemSrc) map[string]any {
	return map[string]any{"name": o.Name, "is_dir": o.IsDir, "size": o.Size, "modified": o.Modified}
}

// parseItems 解析 OpenList content 数组。
func parseItems(data json.RawMessage) []mapItemSrc {
	var wrap struct {
		Content []mapItemSrc `json:"content"`
		Total   int64        `json:"total"`
	}
	_ = json.Unmarshal(data, &wrap)
	return wrap.Content
}

// relayFail 上游错误归一（仅透传可读文案；object not found 映射 40400）。
func relayFail(w http.ResponseWriter, err error) {
	var ue *UpstreamError
	if errors.As(err, &ue) && ue.Exposable {
		if strings.Contains(strings.ToLower(ue.Message), "not found") {
			writeFail(w, http.StatusNotFound, codeNotFound, "文件或目录不存在", nil)
			return
		}
		status := ue.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		code := ue.Code
		if code == 0 {
			code = codeUpstream
		}
		writeFail(w, status, code, ue.Message, nil)
		return
	}
	if errors.Is(err, ErrNotConfigured) {
		writeFail(w, http.StatusServiceUnavailable, codeNotConfigured, ErrNotConfigured.Error(), nil)
		return
	}
	writeFail(w, http.StatusBadGateway, codeUpstream, "网盘服务异常，请稍后再试", nil)
}

// requireOL OpenList 客户端可用性检查。
func (m *Module) requireOL(w http.ResponseWriter) (OpenListClient, bool) {
	if m.OL == nil {
		writeFail(w, http.StatusServiceUnavailable, codeNotConfigured, "网盘服务异常，请稍后再试", nil)
		return nil, false
	}
	if err := m.OL.EnsureConfigured(); err != nil {
		writeFail(w, http.StatusServiceUnavailable, codeNotConfigured, err.Error(), nil)
		return nil, false
	}
	return m.OL, true
}

// docKey sha1(绝对路径|size|modified)：内容变即滚动（OnlyOffice / 缩略图缓存键同口径）。
func docKey(abs string, size int64, modified string) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%s", abs, size, modified)))
	return hex.EncodeToString(h[:])
}

// signTicket 签发票据（jwt.MapClaims + TTL）。
func (m *Module) signTicket(claims jwt.MapClaims, ttl time.Duration) string {
	claims["iat"] = time.Now().Unix()
	claims["exp"] = time.Now().Add(ttl).Unix()
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(m.JWTSecret))
	return tok
}

// verifyTicket 验发票据；失败返回 nil。
func (m *Module) verifyTicket(token string) jwt.MapClaims {
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(m.JWTSecret), nil
	})
	if err != nil || !parsed.Valid {
		return nil
	}
	return claims
}

func claimsString(claims jwt.MapClaims, key string) string {
	if claims == nil {
		return ""
	}
	v, _ := claims[key].(string)
	return v
}

// maxUploadBytes 单文件上传上限（字节）。
func (m *Module) maxUploadBytes() int64 {
	if m.MaxUploadMB <= 0 {
		return 2048 << 20
	}
	return m.MaxUploadMB << 20
}

func (m *Module) thumbMaxBytes() int64 {
	if m.ThumbMaxMB <= 0 {
		return 20 << 20
	}
	return m.ThumbMaxMB << 20
}
