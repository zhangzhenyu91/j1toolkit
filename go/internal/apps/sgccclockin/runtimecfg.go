// 运行时配置：协议密钥、出口代理、排程时刻从 config.Store 动态读取（库内可覆盖 env）。
// 每次协议调用现读现用——配置变更后下一次调用即热生效，无需重启；
// 密钥/代理指纹变化时清空 uniID 池，避免旧密钥语境的池被带入新密钥请求。
package sgccclockin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"shade/internal/config"
)

// ConfigStore 运行时配置读取（*config.Store 天然满足；测试可注入桩）。
type ConfigStore interface {
	Get(ctx context.Context, key string) (string, error)
}

// ChangeSubscriber 配置变更订阅（*config.Store 满足；非必填，缺省不订阅）。
type ChangeSubscriber interface {
	OnChange(config.Listener)
}

// 编译期契约：*config.Store 同时满足读取与变更订阅。
var (
	_ ConfigStore      = (*config.Store)(nil)
	_ ChangeSubscriber = (*config.Store)(nil)
)

// 运行时配置键（cfg_key 与 env 同名，SeedFromEnv 首次从 env 导入）。
const (
	CfgJWTSecret       = "SGCC_JWT_SECRET"
	CfgAdSecret        = "SGCC_AD_SECRET"
	CfgSM2ServerPub    = "SGCC_SM2_SERVER_PUB"
	CfgSM2ClientPriv   = "SGCC_SM2_CLIENT_PRIV"
	CfgRSAPub          = "SGCC_RSA_PUB"
	CfgRSAPriv         = "SGCC_RSA_PRIV"
	CfgDCUPub          = "SGCC_DCU_PUB"
	CfgWLAPriv         = "SGCC_WLA_PRIV"
	CfgDXAppID         = "SGCC_DX_APPID"
	CfgVersion         = "SGCC_VERSION"
	CfgVersionCode     = "SGCC_VERSION_CODE"
	CfgGrayVersion     = "SGCC_GRAY_VERSION"
	CfgSyncTime        = "SGCC_SYNC_TIME"
	CfgStartRemindTime = "SGCC_STARTCLOCK_REMIND_TIME"
	CfgEndRemindTime   = "SGCC_ENDCLOCK_REMIND_TIME"
	// Deprecated: 旧「成员间固定间隔」已删除（改为并发闸门限流）。键仍可能残留在库里，
	// 但不再读取，也不再出现在 ConfigSpecs 的导入注册表中。
	CfgSyncIntervalMs     = "SGCC_SYNC_INTERVAL_MS"
	CfgSyncConcurrency    = "SGCC_SYNC_CONCURRENCY"  // 同步单元全局并发上限（默认 4，收敛 1–8）
	CfgPhotoConcurrency   = "SGCC_PHOTO_CONCURRENCY" // 照片任务全局并发上限（默认 2，收敛 1–4）
	CfgProxyURL           = "SGCC_PROXY_URL"
	CfgProxyProbeInterval = "SGCC_PROXY_PROBE_INTERVAL_MS"
	CfgAMapKey            = "AMAP_MAP_KEY"
	CfgAMapBase           = "AMAP_BASE_URL"
)

// ConfigSpecs 运行时配置键规格（供装配层 SeedFromEnv 导入 env；敏感项加密存储/脱敏展示）。
func ConfigSpecs() []config.Spec {
	return []config.Spec{
		{Key: CfgJWTSecret, Env: CfgJWTSecret, Sensitive: true},
		{Key: CfgAdSecret, Env: CfgAdSecret, Sensitive: true},
		{Key: CfgSM2ServerPub, Env: CfgSM2ServerPub, Sensitive: true},
		{Key: CfgSM2ClientPriv, Env: CfgSM2ClientPriv, Sensitive: true},
		{Key: CfgRSAPub, Env: CfgRSAPub, Sensitive: true},
		{Key: CfgRSAPriv, Env: CfgRSAPriv, Sensitive: true},
		{Key: CfgDCUPub, Env: CfgDCUPub, Sensitive: true},
		{Key: CfgWLAPriv, Env: CfgWLAPriv, Sensitive: true},
		{Key: CfgDXAppID, Env: CfgDXAppID, Sensitive: true},
		{Key: CfgVersion, Env: CfgVersion, Default: "3.3.7"},
		{Key: CfgVersionCode, Env: CfgVersionCode, Default: "202609232038"},
		{Key: CfgGrayVersion, Env: CfgGrayVersion, Default: "2.4.7.1"},
		{Key: CfgSyncTime, Env: CfgSyncTime, Default: "23:00"},
		{Key: CfgStartRemindTime, Env: CfgStartRemindTime, Default: "11:00"},
		{Key: CfgEndRemindTime, Env: CfgEndRemindTime, Default: "18:00"},
		{Key: CfgSyncConcurrency, Env: CfgSyncConcurrency, Default: "4"},
		{Key: CfgPhotoConcurrency, Env: CfgPhotoConcurrency, Default: "2"},
		{Key: CfgProxyURL, Env: CfgProxyURL, Sensitive: true},
		{Key: CfgProxyProbeInterval, Env: CfgProxyProbeInterval, Default: "300000"},
		{Key: CfgAMapKey, Env: CfgAMapKey, Sensitive: true},
		{Key: CfgAMapBase, Env: CfgAMapBase},
	}
}

// cfgValue 读运行时配置；未接入/读取失败/键不存在返回空串。
func cfgValue(ctx context.Context, cfg ConfigStore, key string) string {
	if cfg == nil {
		return ""
	}
	v, err := cfg.Get(ctx, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// cfgInt 读整型运行时配置；未配置/非法回退 fallback。
func cfgInt(ctx context.Context, cfg ConfigStore, key string, fallback int) int {
	raw := cfgValue(ctx, cfg, key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

// clampInt 收敛到 [lo, hi]。
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// syncConcurrency 同步单元全局并发上限（SGCC_SYNC_CONCURRENCY，默认 4，收敛 1–8）。
func (m *Module) syncConcurrency(ctx context.Context) int {
	return clampInt(cfgInt(ctx, m.Cfg, CfgSyncConcurrency, defaultSyncConcurrency), 1, maxSyncConcurrency)
}

// photoConcurrency 照片任务全局并发上限（SGCC_PHOTO_CONCURRENCY，默认 2，收敛 1–4）。
func (m *Module) photoConcurrency(ctx context.Context) int {
	return clampInt(cfgInt(ctx, m.Cfg, CfgPhotoConcurrency, defaultPhotoConcurrency), 1, maxPhotoConcurrency)
}

// resolve 解析当前生效的协议密钥与出口代理：Cfg 读到的值覆盖静态字段（库内可覆盖 env），
// 键不存在/读取失败回退静态字段。每次调用现读，配置变更后下一次调用即热生效。
func (p *Protocol) resolve(ctx context.Context) (Keys, string) {
	k := p.Keys
	proxy := p.ProxyURL
	if p.Cfg == nil {
		return k, proxy
	}
	overlay := func(key string, dst *string) {
		if v, ok := p.cfgGet(ctx, key); ok {
			*dst = v
		}
	}
	overlay(CfgJWTSecret, &k.JWTSecret)
	overlay(CfgAdSecret, &k.AdSecret)
	overlay(CfgSM2ServerPub, &k.SM2ServerPub)
	overlay(CfgSM2ClientPriv, &k.SM2ClientPriv)
	overlay(CfgRSAPub, &k.RSAPub)
	overlay(CfgRSAPriv, &k.RSAPriv)
	overlay(CfgDCUPub, &k.DCUPub)
	overlay(CfgWLAPriv, &k.WLAPriv)
	overlay(CfgVersion, &k.Version)
	overlay(CfgVersionCode, &k.VersionCode)
	overlay(CfgGrayVersion, &k.GrayVersion)
	// 代理键区分「未配置」与「已清空」：库内存在（含空串）即权威——空串=关闭代理
	if v, ok := p.cfgGet(ctx, CfgProxyURL); ok {
		proxy = v
	}
	return k, proxy
}

// cfgGet 读单键；键存在（含空值）返回 ok=true，不存在/失败返回 ok=false。
func (p *Protocol) cfgGet(ctx context.Context, key string) (string, bool) {
	v, err := p.Cfg.Get(ctx, key)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(v), true
}

// syncFingerprint 比对密钥/代理指纹，变化时清空 uniID 池（旧密钥语境的池不带入新密钥请求）。
// 在持有 p.mu 时调用。
func (p *Protocol) syncFingerprint(k Keys, proxy string) {
	fp := keysFingerprint(k, proxy)
	if p.fp != "" && p.fp != fp {
		p.poolM = nil
		p.poolX = nil
	}
	p.fp = fp
}

// fingerprint 锁内刷新并取当前密钥/代理指纹（供补池单飞写回时校验配置未变）。
func (p *Protocol) fingerprint(k Keys, proxy string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.syncFingerprint(k, proxy)
	return p.fp
}

// keysFingerprint 密钥/代理指纹（sha256 十六进制）。
func keysFingerprint(k Keys, proxy string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		k.JWTSecret, k.AdSecret, k.SM2ServerPub, k.SM2ClientPriv,
		k.RSAPub, k.RSAPriv, k.DCUPub, k.WLAPriv,
		k.Version, k.VersionCode, k.GrayVersion, proxy,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}
