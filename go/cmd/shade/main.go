// Shade 壹匣 Go 服务装配入口：统一登录 + 全业务模块挂载 + 后台排程启停。
// 启动最早处替换全进程共享 HTTP Transport（退出关闭空闲连接）；生产启动顺序：MySQL/Redis →
// 系统表（sys_team/sys_user/sys_app/sys_user_app 建表与老库补列）→
// 运行配置（sys_runtime_config / app_log）→ 首次 env 补缺（含 sgccclockin.ConfigSpecs 合并）→
// 通知表/sys_app 应用种子/账号自举 → 安全日模板库（{SAFEDAY_DATA_DIR}/templates）→
// 各模块建表（依赖序：worklog 先于 teamdata，商旅表依赖 worklog_photo 全列）→
// 出工成员启动补缺 → 路由装配（全部 logger/访问日志共用文件持久层 + 唯一 Importer）→ 排程与照片清扫启动。
// 关停顺序：停止 HTTP → 逆序 cleanup（排程/微信/清扫 → 日志文件排空 + fsync）→ 关 DB；
// 在飞后台任务为 best-effort，可能被中断（不等待全部生产者 join）。
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"shade/internal/apps/adminapi"
	"shade/internal/apps/authwx"
	"shade/internal/apps/bridge"
	"shade/internal/apps/callme"
	"shade/internal/apps/chatlog"
	"shade/internal/apps/crossing"
	"shade/internal/apps/kvm"
	"shade/internal/apps/netdisk"
	"shade/internal/apps/office"
	"shade/internal/apps/quiz"
	"shade/internal/apps/safeday"
	"shade/internal/apps/sgccclockin"
	"shade/internal/apps/teamdata"
	"shade/internal/apps/treehazard"
	"shade/internal/apps/wechatanswer"
	"shade/internal/apps/wmadd"
	"shade/internal/apps/worklog"
	"shade/internal/auth"
	"shade/internal/config"
	"shade/internal/httpapi"
	"shade/internal/integrations"
	"shade/internal/logging"
	"shade/internal/logsafe"

	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// startupSink 启动期 DurableSink（fatal 日志按 error 级别写文件并 fsync；未初始化时只写 stderr）。
var startupSink *logging.DurableSink

// recordFatal 以 error 级别写 runtime.fatal（DurableSink urgentEntry 会 fsync），不改变退出行为。
func recordFatal(sink *logging.DurableSink, msg string) {
	if sink == nil {
		return
	}
	_ = logging.NewWithSink(sink, "system").Record(context.Background(),
		"error", "runtime", "runtime.fatal", msg, msg, nil)
}

// fatalLog 启动/运行致命错误：先同步写 DurableSink（error 级别 fsync），再 stderr + os.Exit(1)。
func fatalLog(v ...any) {
	msg := logsafe.TextForLog(fmt.Sprint(v...))
	recordFatal(startupSink, msg)
	_, _ = fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

// fatalLogf 格式化版本的 fatalLog。
func fatalLogf(format string, args ...any) { fatalLog(fmt.Sprintf(format, args...)) }

// 连接池默认上限：32 路并发/16 条空闲；MYSQL_MAX_OPEN_CONNS / MYSQL_MAX_IDLE_CONNS 可覆盖。
const (
	defaultMySQLMaxOpenConns = 32
	defaultMySQLMaxIdleConns = 16
)

// mysqlDSN 拼接 MySQL DSN：MYSQL_DSN 直填原样（参数由运维自行决定）；
// 自动拼装分支带拨号超时 5s 与客户端参数插值（减少预处理往返，避免高并发下额外 RTT）。
func mysqlDSN() string {
	if dsn := os.Getenv("MYSQL_DSN"); dsn != "" {
		return dsn
	}
	user := os.Getenv("MYSQL_USER")
	password := os.Getenv("MYSQL_PASSWORD")
	host := env("MYSQL_HOST", "127.0.0.1")
	port := env("MYSQL_PORT", "3306")
	database := os.Getenv("MYSQL_DATABASE")
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&charset=utf8mb4&loc=Local&timeout=5s&interpolateParams=true", user, password, host, port, database)
}

// envInt 读正整数 env；空/非法/非正值回退 fallback 并告警。
func envInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		log.Printf("[启动] %s=%q 无效，使用默认 %d", name, raw, fallback)
		return fallback
	}
	return n
}

// mysqlPoolLimits 连接池上限（默认 32/16，env 可覆盖）。
func mysqlPoolLimits() (maxOpen, maxIdle int) {
	return envInt("MYSQL_MAX_OPEN_CONNS", defaultMySQLMaxOpenConns),
		envInt("MYSQL_MAX_IDLE_CONNS", defaultMySQLMaxIdleConns)
}

// configurePool 应用连接池参数：上限 32/16、连接寿命 30m、空闲连接 5m 回收
// （避免长期空闲连接被数据库/中间设备单方面断开后再被复用）。
func configurePool(db *sql.DB) {
	maxOpen, maxIdle := mysqlPoolLimits()
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
}

func openDatabase() (*sql.DB, error) {
	dsn := mysqlDSN()
	if os.Getenv("MYSQL_DATABASE") == "" && os.Getenv("MYSQL_DSN") == "" {
		return nil, fmt.Errorf("MYSQL_DATABASE 未配置；本地验证可显式设置 SHADE_LOCAL_FIXTURE=1")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	configurePool(db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("MySQL 连接失败: %w", err)
	}
	return db, nil
}

// JWT 有效期缺省值（JWT_EXPIRES 7d / JWT_WEB_EXPIRES 12h，同 README 与运行配置注册表）。
const (
	defaultJWTExpires    = 7 * 24 * time.Hour
	defaultJWTWebExpires = 12 * time.Hour
)

// jwtTokenTTLs 解析 JWT_EXPIRES / JWT_WEB_EXPIRES（支持 7d/12h 等写法）；
// 空值或非法值回退缺省并告警，保证装配总能拿到正数有效期。
func jwtTokenTTLs(getenv func(string) string) (expires, webExpires time.Duration) {
	expires = parseJWTTTL(getenv("JWT_EXPIRES"), defaultJWTExpires, "JWT_EXPIRES")
	webExpires = parseJWTTTL(getenv("JWT_WEB_EXPIRES"), defaultJWTWebExpires, "JWT_WEB_EXPIRES")
	return expires, webExpires
}

// parseJWTTTL 单键解析：空值静默用缺省，非法/非正值告警后回退缺省。
func parseJWTTTL(raw string, fallback time.Duration, name string) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	ttl, err := auth.ParseTokenTTL(raw)
	if err != nil || ttl <= 0 {
		log.Printf("[启动] %s=%q 非法，使用默认 %s", name, raw, fallback)
		return fallback
	}
	return ttl
}

// cfgOrEnv 读运行时配置（库内优先），键缺失/为空回退 env（静态注入场景：GLKVM、商旅密钥等）。
func cfgOrEnv(ctx context.Context, store *config.Store, key string) string {
	if store != nil {
		if v, err := store.Get(ctx, key); err == nil && v != "" {
			return v
		}
	}
	return os.Getenv(key)
}

// wechatReconnectKeys 触发微信智能应答重连的运行配置键。
var wechatReconnectKeys = map[string]bool{
	"WECHATPADPRO_TOKEN": true, "WECHATPADPRO_API_BASE": true, "WECHATPADPRO_WS_URL": true,
	"WECHATPADPRO_TICKET_PATH": true, "WECHATPADPRO_BOT_WXID": true, "WECHATPADPRO_FILE_APP_ID": true,
}

// wechatReconnectFromEvents 批量变更过滤：一次 UpdateBatch 无论命中多少个微信键，只重连一次。
func wechatReconnectFromEvents(events []config.ChangeEvent, reconnect func()) bool {
	for _, ev := range events {
		if wechatReconnectKeys[ev.Key] {
			reconnect()
			return true
		}
	}
	return false
}

// registerWechatReconnect 注册批量变更监听；单键 Update/Seed 不触发（管理端保存统一走 UpdateBatch）。
func registerWechatReconnect(store *config.Store, reconnect func()) {
	store.OnChangeBatch(func(events []config.ChangeEvent) {
		wechatReconnectFromEvents(events, reconnect)
	})
}

// wechatPermit 微信智能应答为仅超管开放的管理应用；
// 其余应用键原样走 sys_user_app。
type wechatPermit struct{ db *sql.DB }

func (p wechatPermit) Allow(ctx context.Context, userID int64, appKey string) (bool, error) {
	if appKey == wechatanswer.AppKey {
		var role string
		err := p.db.QueryRowContext(ctx, `SELECT role FROM sys_user WHERE id = ? AND status = 1`, userID).Scan(&role)
		if err == nil {
			return role == "admin", nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, httpapi.DependencyUnavailable("微信智能应答权限读取失败")
	}
	return httpapi.SQLAppPermit{DB: p.db}.Allow(ctx, userID, appKey)
}

// appLogSettings 启动期日志文件参数：目录 + 批量/fsync 策略（不进入运行配置注册表）。
type appLogSettings struct {
	Dir     string
	Options logging.DurableOptions
}

// appLogSettingsFromEnv 读取 APP_LOG_DIR / APP_LOG_BATCH_SIZE / APP_LOG_FSYNC，非法值回退默认。
func appLogSettingsFromEnv() appLogSettings {
	return appLogSettings{Dir: env("APP_LOG_DIR", "./data/logs"), Options: logging.DurableOptionsFromEnv()}
}

// appLogDBIDs 读取全部非空 log_id 供全量导出对账（仅超管全量导出调用；DB 故障由调用方降级）。
func appLogDBIDs(db *sql.DB) func(context.Context) (map[string]struct{}, error) {
	return func(ctx context.Context) (map[string]struct{}, error) {
		rows, err := db.QueryContext(ctx, `SELECT log_id FROM app_log WHERE log_id <> ''`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		ids := make(map[string]struct{}, 1024)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return ids, nil
	}
}

// cleanupAppLogDB 仅当待删区间内每一条 DB 行的 log_id 都能在文件段中找到时才清理数据库；
// 返回 -1 表示 fail-closed（DB-only 行/空 log_id/文件坏行半行/历史空洞）。
// 仅凭日期存在不代表覆盖：同一日期内可能只有部分行有文件副本。
// 归档保留期短于数据库保留期时由调用方提前停用，避免删掉唯一副本。
func cleanupAppLogDB(ctx context.Context, db *sql.DB, durable *logging.DurableSink, retainDays int) (int64, error) {
	if retainDays == 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -retainDays)
	ids, err := config.AppLogIDsBefore(ctx, db, cutoff)
	if err != nil {
		return -1, fmt.Errorf("读取待清理日志 log_id 失败: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	fileIDs, err := durable.LogIDSet(ctx)
	if err != nil {
		return -1, fmt.Errorf("读取文件 log_id 覆盖失败，跳过数据库清理: %w", err)
	}
	var missing []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			missing = append(missing, "<空 log_id>")
			continue
		}
		if _, ok := fileIDs[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sample := missing
		if len(sample) > 10 {
			sample = sample[:10]
		}
		return -1, fmt.Errorf("文件缺少 %d 条待删 DB 行的 log_id 副本（示例: %s），跳过数据库清理",
			len(missing), strings.Join(sample, "、"))
	}
	return config.CleanupAppLog(ctx, db, retainDays)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	// 文件持久日志层：不依赖数据库，在 main 最早期创建；每条日志先立即写 NDJSON，
	// 数据库接入后由唯一 Importer 异步补录（DB 故障/未连接不影响文件落盘）。
	logSettings := appLogSettingsFromEnv()
	durable, err := logging.NewDurableSinkWithOptions(logSettings.Dir, logSettings.Options)
	if err != nil {
		fatalLogf("[启动] 初始化日志持久层失败: %v", err)
	}
	startupSink = durable
	// 标准库 log 统一接入文件持久层：DB 初始化前的启动日志也先同步落盘；致命错误走 fatalLog（error 级 fsync）。
	restoreLogOutput := logging.InstallStandardLogger(durable, "system")
	defer restoreLogOutput()
	// 出站观测写入器：SetDefaultTransport 之后所有 client 的调用事件（2xx/失败）写同一文件持久层。
	integrations.SetObservedRecorder(logging.NewWithSink(durable, "system"))
	var cleanups []func()
	// 最早登记 → cleanups 逆序执行时最后运行：生产者全部停止后先排空/fsync 文件，
	// 再停止 Importer，最后才执行 defer 的 db.Close。
	cleanups = append(cleanups, func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dcancel()
		if err := durable.CloseContext(dctx); err != nil {
			log.Printf("[关闭] 日志文件排空未在预算内完成（游标保留，下次启动补录）: %v", err)
		}
	})
	// 全进程共享连接池：现有 http.Client{}（Transport 为 nil）立即复用；
	// 退出时关闭空闲连接（defer 最后执行，在日志排空与 DB 关闭之后）。
	sharedTransport := integrations.SetDefaultTransport()
	defer sharedTransport.CloseIdleConnections()
	var deps httpapi.Deps
	var db *sql.DB
	var modules []httpapi.Module
	var cfgStore httpapi.ConfigValue
	var officeMod *office.Module
	// 访问日志中间件选项：非 fixture 时注入文件持久层与同一 auth.Service；
	// fixture 为零值（WithAccessLogOptions 原样透传，不落日志）。
	var accessLogOpts httpapi.AccessLogOptions
	if os.Getenv("SHADE_LOCAL_FIXTURE") == "1" {
		deps = httpapi.FixtureDeps()
		deps.Auth = auth.FixtureAuth{}
		log.Printf("[启动] 使用本地 fixture，仅用于开发验证，不连接 MySQL")
	} else {
		var err error
		db, err = openDatabase()
		if err != nil {
			fatalLog(err)
		}
		defer db.Close()
		jwtSecret := os.Getenv("JWT_SECRET")
		if jwtSecret == "" {
			fatalLog("JWT_SECRET 未配置")
		}
		redisClient := redis.NewClient(&redis.Options{
			Addr:     fmt.Sprintf("%s:%s", env("REDIS_HOST", "127.0.0.1"), env("REDIS_PORT", "6379")),
			Password: os.Getenv("REDIS_PASSWORD"),
		})
		defer redisClient.Close()
		cipher, err := config.NewConfigCipher(os.Getenv("CONFIG_MASTER_KEY"), jwtSecret)
		if err != nil {
			fatalLog(err)
		}
		store := config.NewStore(db, cipher)
		cfgStore = store
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// schema 依赖顺序：系统表（空库建表/老库补列）→ 配置/日志/通知表 → 应用种子 → 账号自举
		// → 各业务模块（worklog 先于 teamdata/sgcc）
		if err := httpapi.EnsureSystemSchema(ctx, db); err != nil {
			fatalLog(err)
		}
		if err := store.EnsureSchema(ctx); err != nil {
			fatalLog(err)
		}
		// app_log 迁移使用独立 10 分钟预算（不占用 30s 普通建表 ctx）；老库回填分批执行。
		appLogCtx, appLogCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		if err := config.EnsureAppLogSchema(appLogCtx, db); err != nil {
			appLogCancel()
			fatalLog(err)
		}
		if err := config.BackfillAppLog(appLogCtx, db, 5000); err != nil {
			log.Printf("[启动] app_log 回填未完成（下次启动继续）: %v", err)
		}
		appLogCancel()
		// 数据库就绪后接入唯一 Importer：启动早期的文件日志从此处开始补录。
		durable.AttachDB(db)
		if err := httpapi.EnsureNoticeSchema(ctx, db); err != nil {
			fatalLog(err)
		}
		// 应用种子先于账号自举：管理员默认授权按 app_key 行补缺，种子不在位会静默跳过
		if err := httpapi.SeedAppExtras(ctx, db); err != nil {
			fatalLog(err)
		}
		if err := httpapi.BootstrapAccounts(ctx, db, os.Getenv("ADMIN_USERNAME"), os.Getenv("ADMIN_PASSWORD")); err != nil {
			fatalLog(err)
		}
		// 首次 env 补缺：注册表合并 sgccclockin.ConfigSpecs（同键模块规格优先）
		specs := config.MergeSpecs(config.DefaultSpecs(), sgccclockin.ConfigSpecs())
		seeded, err := store.SeedFromEnv(ctx, specs, os.Getenv)
		if err != nil {
			fatalLog(err)
		}
		log.Printf("[启动] 运行配置初始化完成，首次补缺 %d 项", seeded)

		// JWT 时效真实接入：库内运行配置优先、env 兜底（支持 7d/12h），非法值回退缺省并告警。
		expiresTTL, webTTL := jwtTokenTTLs(func(key string) string { return cfgOrEnv(ctx, store, key) })
		authService := auth.NewService(auth.SQLUserStore{DB: db}, auth.RedisBlacklist{Client: redisClient}, jwtSecret, expiresTTL, webTTL)
		permit := httpapi.SQLAppPermit{DB: db}
		// 应用日志、integrations recorder 与 HTTP 访问日志共用 main 早期创建的文件持久层；
		// 入库只由 durable 的唯一 Importer 负责（旧 Queue 已退出主写入路径，满队不再丢 info）。
		// 访问日志与应用日志同一 auth.Service（与 AuthMiddleware 同口径，
		// 公开下载路径仍可凭 DB 兜底构造验签归属）。
		accessLogOpts = httpapi.AccessLogOptions{Sink: durable, Auth: authService, DB: db, Events: logging.NewWithSink(durable, "system")}
		// 系统公共事件与访问日志归 system，具体应用事件使用各自的 sys_app 键。
		appLog := logging.NewWithSink(durable, "system")
		// 黑名单（Redis）故障告警：Authenticate 已 fail-closed，这里补可检索的结构化错误日志。
		authService.OnBlacklistError = func(err error) {
			msg := logsafe.TextForLog("鉴权黑名单存储故障，受保护请求按未登录处理: " + err.Error())
			log.Printf("[鉴权] %s", msg)
			_ = appLog.Error(context.Background(), "security", "security.blacklist.unavailable",
				"鉴权黑名单存储故障，受保护请求按未登录处理", msg, nil)
		}
		provider := integrations.NewRuntimeProvider(store)
		cosStore := &integrations.COSStore{Cfg: provider, Recorder: logging.NewWithSink(durable, "system")}
		amap := &integrations.AMapService{Cfg: provider, Recorder: logging.NewWithSink(durable, "work-log")}
		// 照片识别：worklog.recognize 业务层已有 4 轮重试，传输层显式固定单次，
		// 避免「业务 4 轮 × 传输 3 次 = 12 次」放大；总耗时另受 worklog 识别时间预算约束。
		worklogVerifier := integrations.NewPhotoVerifier(&integrations.WorkflowClient{
			Cfg: provider, APIKeyKey: "DIFY_WORKLOG_API_KEY",
			Retry:    integrations.RetryPolicy{MaxAttempts: 1},
			Recorder: logging.NewWithSink(durable, "work-log"),
		})
		safedayRunner := &integrations.StreamRunner{Client: &integrations.WorkflowClient{Cfg: provider, APIKeyKey: "DIFY_SAFEDAY_API_KEY", Recorder: logging.NewWithSink(durable, "safe-day")}}
		weknora := callme.NewHTTPClient(store)
		// WeChatPadPro 云端网关客户端（bridge 集中构造）：商旅通知微信侧用；wechatanswer
		// 五通道由 bridge.Wire 内部另建实例注入（客户端无会话状态，多例等价）
		padPro := bridge.NewPadPro(store)
		padPro.Log = logging.NewWithSink(durable, "wechat-answer")

		deps = httpapi.Deps{
			Apps:     httpapi.SQLAppCatalog{DB: db},
			Cfg:      httpapi.StoreConfigAdmin{Store: store},
			Logs:     httpapi.SQLLogReader{DB: db},
			Auth:     authService,
			Accounts: httpapi.SQLAccountStore{DB: db},
			Notices:  httpapi.SQLNoticeReader{DB: db},
			Events:   logging.NewWithSink(durable, "system"),
		}

		// ---- 业务模块（构造不分先后，建表按依赖序执行）----
		// 商旅打卡（出工日志扩展；任务队列供 worklog 复用）
		sgc := sgccclockin.New(db)
		sgc.Permit = permit
		sgc.Cfg = store
		// 模块内 appLog 在 Log 为 nil 时会自建直写 logger；显式接同一文件持久层，避免绕过 Importer。
		sgc.Log = logging.NewWithSink(durable, "sgcc-clockin")
		proto := sgccclockin.NewProtocol(sgccclockin.Keys{
			JWTSecret:     cfgOrEnv(ctx, store, "SGCC_JWT_SECRET"),
			AdSecret:      cfgOrEnv(ctx, store, "SGCC_AD_SECRET"),
			SM2ServerPub:  cfgOrEnv(ctx, store, "SGCC_SM2_SERVER_PUB"),
			SM2ClientPriv: cfgOrEnv(ctx, store, "SGCC_SM2_CLIENT_PRIV"),
			RSAPub:        cfgOrEnv(ctx, store, "SGCC_RSA_PUB"),
			RSAPriv:       cfgOrEnv(ctx, store, "SGCC_RSA_PRIV"),
			DCUPub:        cfgOrEnv(ctx, store, "SGCC_DCU_PUB"),
			WLAPriv:       cfgOrEnv(ctx, store, "SGCC_WLA_PRIV"),
			Version:       cfgOrEnv(ctx, store, "SGCC_VERSION"),
			VersionCode:   cfgOrEnv(ctx, store, "SGCC_VERSION_CODE"),
			GrayVersion:   cfgOrEnv(ctx, store, "SGCC_GRAY_VERSION"),
		})
		proto.Cfg = store
		sgc.API = proto
		// 商旅通知（Notifier）：站内 sys_notice（SQLNoticeWriter）+ 微信（PadPro SendText）+ 出口代理地址展示
		noticeWriter := bridge.SQLNoticeWriter{DB: db}
		sgc.Notifier = &sgccclockin.PushNotifier{
			DB: db,
			InApp: func(ctx context.Context, userIDs []int64, roles []string, title, content string) error {
				return noticeWriter.PushNotice(ctx, roles, userIDs, title, content)
			},
			Wx:       sgccclockin.WxSendFrom(padPro),
			WxAt:     padPro.SendTextAt,
			ProxyURL: func(ctx context.Context) string { return cfgOrEnv(ctx, store, "SGCC_PROXY_URL") },
		}

		// 出工日志（worklog_* 表归本模块建，teamdata/sgcc 依赖）
		wl := worklog.New(db)
		wl.Cfg = store
		wl.Store = cosStore
		wl.Dify = worklogVerifier
		wl.Geo = amap
		wl.Permit = permit
		wl.SgccOps = sgc.Ops // 商旅照片任务锁（删卡/传图前拦截）
		wl.SgccRemote = sgc  // 商旅费用照片远端联动（上传/调人名/删除远端先行）
		wl.PhotoOps = sgc    // 商旅照片任务队列（异步任务化，端侧凭 opId 轮询）
		// Notifier 人工审核通知 / Dispatcher 派车单取件建卡 / Netdisk 保存：由下方 bridge.Wire 注入

		// 费用照片双向对账生产装配（Node syncFeePhotos 同链路）：COS 对象存储（拉下入库/删除对账）
		// + 水印识别回写钩子（worklog.VerifyAndWriteBack，拉下入库后异步触发，含失败班组通知）
		sgc.Store = cosStore
		sgc.VerifyPhoto = wl

		// 班组数据（复用 worklog 字典表 + team_line）
		td := teamdata.New(db, store)
		td.Permit = permit

		// 树障隐患
		th := treehazard.New(db)
		th.Cfg = store
		th.Objects = cosStore
		th.Permit = permit
		th.Files = treehazard.LocalBatchFiles{} // 随批 xlsx 落盘 data/treehazard/inbox/（Node 同口径）
		// Usb 设备链路 / 同步结果通知由下方 bridge.Wire 注入；
		// WeKnora / WeKnoraState 留 nil 走模块内置 HTTP 客户端与默认状态文件（WEKNORA_* 动态取值）

		// 交跨记录
		cr := crossing.New(db, store)
		cr.Permit = permit
		cr.ServerDir = "" // 随批 xlsx 落盘 data/crossing/inbox/（空=当前目录，Node server 目录口径）
		// Usb / 同步通知 / WeKnora 同树障（bridge.Wire 注入 / 内置客户端）

		// 团队网盘
		nd := netdisk.New(db)
		nd.OL = &netdisk.OpenListHTTP{Cfg: store}
		nd.Cfg = store
		nd.JWTSecret = jwtSecret
		nd.Permit = permit
		nd.Auth = authService
		nd.Thumb = netdisk.NewWebpThumbnailer()
		// 缩略图缓存目录与上传限额（启动时取值：库内运行配置优先、env 兜底；注册表默认 ./data/thumbs / 1024 / 50）
		nd.ThumbDir = cfgOrEnv(ctx, store, "NETDISK_THUMB_DIR")
		nd.MaxUploadMB = parseInt64Cfg(cfgOrEnv(ctx, store, "NETDISK_MAX_UPLOAD_MB"), 0)
		nd.ThumbMaxMB = parseInt64Cfg(cfgOrEnv(ctx, store, "NETDISK_THUMB_MAX_MB"), 0)

		// ONLYOFFICE（Sources / NetdiskDoc 由下方 bridge.Wire 注入，缺模块时对应源报未接入；
		// DB 用于 /config 前按身份收敛生效班组，普通用户/班管传他班 team_id 直接 403）
		of := office.New(store)
		of.Permit = permit
		of.DB = db
		// staging 目录可由环境变量覆盖（默认持久数据目录 data/office-staging，janitor 启动即清理）。
		of.SaveTempDir = env("OO_SAVE_TEMP_DIR", "")
		officeMod = of

		// 题库刷题
		qz := quiz.New(db)
		qz.Permit = permit
		qz.Cfg = store
		qz.Log = logging.NewWithSink(durable, "quiz")
		qz.AdminUsername = env("ADMIN_USERNAME", "admin")
		qz.SetAnalyzer(&quiz.DifyAnalyzer{Cfg: store})

		// Call Me（WeKnora 问答）
		cm := callme.New(db, store)
		cm.WeKnora = weknora
		cm.Permit = permit

		// 聊天日志归档（WeKnora 知识库直推）
		cl := chatlog.New(store, weknora)

		// 安全日活动记录
		rs, err := safeday.NewRecordStore(env("SAFEDAY_DATA_DIR", "./data/safeday"))
		if err != nil {
			fatalLog(err)
		}
		sd := safeday.New(db, rs)
		// 旧安全日记录无 team 字段：按首个启用班组回填（幂等；失败仅告警不阻断启动）。
		if n, err := sd.BackfillLegacyTeams(ctx); err != nil {
			log.Printf("[安全日] 旧记录班组回填失败: %v", err)
		} else if n > 0 {
			log.Printf("[安全日] 已为 %d 条旧记录回填默认班组", n)
		}
		sd.Permit = permit
		sd.Cfg = store
		sd.Auth = authService
		sd.Log = logging.NewWithSink(durable, "safe-day")
		sd.Dify = safedayRunner
		sd.Converter = &safeday.LibreOfficeConverter{
			SofficePath: cfgOrEnv(ctx, store, "SAFEDAY_SOFFICE_PATH"),
			Log:         logging.NewWithSink(durable, "safe-day"),
		}
		sd.COS = cosStore
		// 模板库与记录同根（{SAFEDAY_DATA_DIR}/templates），复用同一 ConfigCipher 加密模板 API Key；
		// index.json 损坏 fail-fast（不静默丢模板配置），错误信息不含任何 API key。
		tplStore, err := safeday.NewTemplateStore(env("SAFEDAY_DATA_DIR", "./data/safeday"), cipher)
		if err != nil {
			fatalLog(err)
		}
		sd.Templates = tplStore

		wxAuth := authwx.New(db, store, authService)
		wxAuth.Events = logging.NewWithSink(durable, "system")
		admin := adminapi.New(db)
		admin.Log = logging.NewWithSink(durable, "system")
		admin.COS = cosStore
		// 员工管理变更 → 出工成员字典同步（Node worklog/member-sync.syncMemberForUser），
		// 含调班迁移与商旅账号跟随；失败仅告警，不影响管理端响应
		admin.OnUserChanged = func(ctx context.Context, userID int64) {
			if err := wl.SyncMemberForUser(ctx, userID); err != nil {
				log.Printf("[出工日志] 成员同步失败（用户 %d）：%v", userID, err)
			}
		}
		// 班组改名级联（Node safeday/migrate.renameTeamFolder）：安全日记录 JSON 历史保留并改写
		// team 字段 + docs 班组子目录更名；网盘公共区 Go 侧每次现查班组名无缓存，无需失效
		admin.OnTeamRenamed = func(oldName, newName string) {
			if n := rs.RenameTeam(oldName, newName); n > 0 {
				log.Printf("[安全日] 班组「%s」改名「%s」：已级联 %d 条记录", oldName, newName, n)
			}
			renameTeamDocsDir(rs.DocsDir(), oldName, newName)
		}
		admin.HasTeamSafedayRecords = func(teamName string) bool { return len(rs.List(teamName)) > 0 }

		// 远程连接计算机（GLKVM + Redis 设备维护锁）
		glkvmClient := kvm.NewGLKVMClient(cfgOrEnv(ctx, store, "GLKVM_URL"), cfgOrEnv(ctx, store, "GLKVM_PASSWORD"))
		glkvmClient.Cfg = store // GLKVM_URL / GLKVM_PASSWORD 运行时配置动态取值，改值即生效（清会话缓存）
		kv := kvm.New(db, glkvmClient)
		kv.Permit = permit
		kv.Locker = &kvm.RedisLocker{Client: redisClient}

		// 水印添加
		wm := wmadd.New(db)
		wm.Permit = permit
		wmGeo := wmadd.NewAMapClient(cfgOrEnv(ctx, store, "AMAP_MAP_KEY"), cfgOrEnv(ctx, store, "AMAP_BASE_URL"))
		wmGeo.Cfg = store
		wm.Geo = wmGeo
		wm.Renderer = &wmadd.WatermarkRenderer{}

		// 微信群智能应答（管理路径统一 /api/v1/wechat-answer）
		wa := wechatanswer.New(db, store)
		wa.Permit = wechatPermit{db: db}
		wa.Rules = wechatanswer.SQLRuleProvider{DB: db}
		wa.State = &wechatanswer.SQLRecordStore{DB: db}
		wa.Queue = &wechatanswer.SQLPushQueue{DB: db}
		wa.Archiver = &wechatanswer.SQLChatArchiver{DB: db} // 未消费聊天归档（写 wa_chat_* 表）
		wa.AppLog = logging.NewWithSink(durable, "wechat-answer")
		registerWechatReconnect(store, func() { wa.Reconnect(context.Background()) })
		// Sender/Members/Images/Files/Sync 五通道与 Ask/Chatlog 直调出口由下方 bridge.Wire 集中注入

		// ---- 跨模块桥（go/internal/apps/bridge）：office 五源/网盘文档源、worklog/safeday
		// 网盘保存、kvm 网盘互推、WF3 命名、微信智能应答五通道/提问/归档、tree/crossing Usb
		// 与结果通知、worklog 人工审核通知与派车单取件建卡、捕获出口 ----
		wa.Sink = bridge.Wire(bridge.Deps{
			Office: of, Worklog: wl, Safeday: sd, Tree: th, Crossing: cr, Kvm: kv, Netdisk: nd,
			Callme: cm, Chatlog: cl, Wechat: wa,
			Cfg: store, DB: db, Platform: glkvmClient, Locker: kv.Locker,
			AdminUsername: env("ADMIN_USERNAME", "admin"),
			Log:           logging.NewWithSink(durable, "wechat-answer"),
		})

		// ---- 建表（依赖序）----
		for _, ensure := range []struct {
			name string
			fn   func(context.Context) error
		}{
			{"worklog", wl.EnsureSchema},
			{"teamdata", td.EnsureSchema},
			{"treehazard", th.EnsureSchema},
			{"crossing", cr.EnsureSchema},
			{"netdisk", nd.EnsureSchema},
			{"quiz", qz.EnsureSchema},
			{"wechatanswer", wa.EnsureSchema},
			{"sgccclockin", sgc.EnsureSchema},
		} {
			if err := ensure.fn(ctx); err != nil {
				fatalLogf("[启动] %s 建表失败: %v", ensure.name, err)
			}
		}

		if n, err := wl.SeedDefaultTowers(ctx); err != nil {
			log.Printf("[启动] 杆塔坐标种子迁移失败（可后续用 Excel 导入）：%v", err)
		} else if n > 0 {
			log.Printf("[启动] 已迁入杆塔坐标 %d 条（默认班组）", n)
		}

		// 出工成员字典启动补缺（Node member-sync.syncAllUserMembers 口径：只补缺失，
		// 不改既有行的启停与姓名；单账号失败仅告警，不阻断启动）
		if err := wl.SyncAllUserMembers(ctx); err != nil {
			log.Printf("[启动] 出工成员同步有失败：%v", err)
		}

		modules = append(modules,
			wxAuth, admin, wl, td, sgc, th, cr, nd, of, qz, cm, cl, sd, kv, wm, wa,
		)

		// pending 照片超时清扫：启动即跑一次，此后每 10 分钟（loadEntries 已不再顺手 UPDATE，
		// 超时自愈依赖本清扫）。用长期 ctx（启动期 ctx 30s 后即到期）；stop 幂等，登记为
		// cleanup 后逆序执行，位于日志文件排空之前。
		cleanups = append(cleanups, wl.StartPhotoSweeper(context.Background()))
		// 安全日生成超时清扫：工作流未升级/回调丢失时把 processing 置 failed，用户可重新生成。
		cleanups = append(cleanups, sd.StartProcessingSweeper(context.Background()))

		// ---- 后台排程：商旅提醒/核查 + 树障/交跨夜间同步 + 派车单每日同步 + 微信智能应答通道 ----
		sched := sgccclockin.NewScheduler(sgc)
		// Start 返回停止函数（幂等）：只登记一次，不另挂 sched.Stop，避免重复 Stop/空排程
		cleanups = append(cleanups, sched.Start())
		if err := wa.Start(context.Background()); err != nil {
			log.Printf("[启动] 微信智能应答后台任务启动失败: %v", err)
		} else {
			cleanups = append(cleanups, func() { _ = wa.Close() })
		}

		// 树障/交跨夜间同步排程：每日 SYNC_PREPARE_TIME（北京时间）起一轮内网 U 盘轮次
		// （准备 snapshot + 挂载 → 等指令窗口 → 取件回写/下发），完成后串行跑 WeKnora 知识库
		// 快照同步（Node 同时刻串行序：先内网取件回写，再同步知识库）；未配置/链路未接入在
		// 触发时明确报出，不假成功。启动只排程不连任何外部服务。
		cleanups = append(cleanups, scheduleDailyCn(
			func(ctx context.Context) string { return cfgOrEnv(ctx, store, "TREEHAZARD_SYNC_PREPARE_TIME") },
			23, 50, syncPair{
				Logger: logging.NewWithSink(durable, "tree-hazard"),
				Cycle: func(ctx context.Context) (string, error) {
					res, err := th.RunIntranetCycle(ctx, "night")
					return res.Message, err
				},
				WeKnora: func(ctx context.Context) (string, error) {
					res, err := th.RunWeKnoraSync(ctx)
					return fmt.Sprintf("建%d/改%d/删%d/跳过%d", res.Created, res.Updated, res.Deleted, res.Skipped), err
				},
			}.Run))
		cleanups = append(cleanups, scheduleDailyCn(
			func(ctx context.Context) string { return cfgOrEnv(ctx, store, "CROSSING_SYNC_PREPARE_TIME") },
			23, 50, syncPair{
				Logger: logging.NewWithSink(durable, "crossing"),
				Cycle: func(ctx context.Context) (string, error) {
					res, err := cr.RunIntranetCycle(ctx, "night")
					return res.Message, err
				},
				WeKnora: func(ctx context.Context) (string, error) {
					res, err := cr.RunWeKnoraSync(ctx)
					return fmt.Sprintf("建%d/改%d/删%d/跳过%d", res.Created, res.Updated, res.Deleted, res.Skipped), err
				},
			}.Run))
		// 派车单每日排程（Node dispatch-sync.start：09:10 准备锁设备+挂载 U 盘 → 09:20 取件建卡解禁）；
		// 生效班组以 worklog_dispatch_sync_team 开关表为准，未配置设备 ddns 在触发时明确报出
		//（不假成功）；执行器与手动 sync-now 共用同一重入守卫（bridge.DispatchSyncer）。
		if wl.Dispatcher != nil {
			jobs := bridge.NewDispatchJobs(wl.Dispatcher)
			disp := dispatchPair{Logger: logging.NewWithSink(durable, "work-log"), Prepare: jobs.Prepare, Fetch: jobs.Fetch}
			cleanups = append(cleanups, scheduleDailyCn(
				func(ctx context.Context) string { return cfgOrEnv(ctx, store, "WORKLOG_DISPATCH_PREPARE_TIME") },
				9, 10, disp.RunPrepare))
			cleanups = append(cleanups, scheduleDailyCn(
				func(ctx context.Context) string { return cfgOrEnv(ctx, store, "WORKLOG_DISPATCH_FETCH_TIME") },
				9, 20, disp.RunFetch))
		}

		// ---- 应用日志保留策略：默认永久（0）；只有明确配置正数且文件归档覆盖时才分批清理 ----
		retentionRaw := cfgOrEnv(ctx, store, "APP_LOG_RETENTION_DAYS")
		retentionDays, retentionErr := config.ParseAppLogRetention(retentionRaw)
		if retentionErr != nil {
			log.Printf("[日志清理] APP_LOG_RETENTION_DAYS=%q 非法，数据库清理保持关闭（永久保留）: %v", retentionRaw, retentionErr)
			retentionDays = 0
		}
		archiveRaw := cfgOrEnv(ctx, store, "APP_LOG_ARCHIVE_RETENTION_DAYS")
		archiveDays, archiveErr := config.ParseAppLogRetention(archiveRaw)
		if archiveErr != nil {
			log.Printf("[日志清理] APP_LOG_ARCHIVE_RETENTION_DAYS=%q 非法，归档清理保持关闭（永久保留）: %v", archiveRaw, archiveErr)
			archiveDays = 0
		}
		if retentionDays > 0 && archiveDays > 0 && archiveDays < retentionDays {
			log.Printf("[日志清理] 跳过数据库清理：归档保留期 %d 天短于数据库保留期 %d 天，可能丢失唯一副本", archiveDays, retentionDays)
			retentionDays = 0
		}
		if retentionDays == 0 && archiveDays == 0 {
			log.Printf("[日志清理] 数据库与文件归档均为永久保留（APP_LOG_RETENTION_DAYS=0、APP_LOG_ARCHIVE_RETENTION_DAYS=0）")
		} else {
			retentionStop := make(chan struct{})
			cleanups = append(cleanups, func() { close(retentionStop) })
			go func() {
				run := func() {
					cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
					defer ccancel()
					if retentionDays > 0 {
						if n, err := cleanupAppLogDB(cctx, db, durable, retentionDays); err != nil {
							log.Printf("[日志清理] 数据库清理跳过/失败: %v", err)
						} else if n >= 0 {
							log.Printf("[日志清理] 数据库清理完成，删除 %d 条", n)
							_ = appLog.Info(cctx, "scheduler", "log.retention.completed",
								"数据库日志清理完成", fmt.Sprintf("[app-log] retention cleanup completed: %d rows", n),
								map[string]any{"deleted": n, "retention_days": retentionDays})
						}
					}
					if archiveDays > 0 {
						n, err := durable.CleanupArchives(archiveDays)
						if err != nil {
							log.Printf("[日志清理] 归档清理失败: %v", err)
						} else if n > 0 {
							log.Printf("[日志清理] 归档清理完成，删除 %d 个段", n)
							_ = appLog.Info(cctx, "scheduler", "log.retention.archive",
								"文件归档清理完成", fmt.Sprintf("[app-log] archive cleanup removed %d segments", n),
								map[string]any{"deleted": n, "retention_days": archiveDays})
						}
					}
				}
				run()
				ticker := time.NewTicker(24 * time.Hour)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						run()
					case <-retentionStop:
						return
					}
				}
			}()
		}

		_ = appLog.Info(context.Background(), "business", "runtime.started", "Go 服务启动完成", "shade go service started", nil)
	}

	// 日志管理端健康与 NDJSON 导出：两种模式都接同一文件持久层（fixture 也可读本地日志目录）。
	// 全量导出时用 DB log_id 对账，发现仅存于数据库的磁盘回退行；DB 不可用不影响文件副本导出。
	logFiles := httpapi.NDJSONLogSource{Dir: logSettings.Dir}
	if db != nil {
		logFiles.DBIDs = appLogDBIDs(db)
	}
	deps.LogFiles = logFiles
	deps.LogHealth = httpapi.DurableLogHealth{Sink: durable}

	port, err := strconv.Atoi(env("PORT", "3000"))
	if err != nil || port < 1 || port > 65535 {
		fatalLogf("PORT 必须是 1-65535 的整数: %v", err)
	}
	handler := httpapi.WithAccessLogOptions(httpapi.WithLegacyPages(httpapi.WithSPA(httpapi.NewRouter(deps, modules...), env("SHADE_WEB_DIR", "../web/dist")), env("SHADE_LEGACY_PUBLIC_DIR", "../server/public"), cfgStore), accessLogOpts)
	// SSE 与长任务不设总写超时；慢速大图上传不设总读取超时，各接口以严格字节上限兜底。
	server := &http.Server{Addr: fmt.Sprintf("0.0.0.0:%d", port), Handler: handler, ReadHeaderTimeout: 15 * time.Second, ReadTimeout: 0, WriteTimeout: 0, IdleTimeout: 120 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Office 保存回调 staging：启动清理崩溃残留，运行期周期清理（ctx 结束停止）。
	if officeMod != nil {
		officeMod.StartSaveTempJanitor(ctx)
	}
	go func() {
		log.Printf("[启动] Shade Go 服务监听 %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatalLog(err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	// 服务退出日志必须在 DurableSink 关闭（cleanups 中最先登记、最后执行）之前写文件。
	log.Printf("[关闭] Shade Go 服务已退出")
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

// renameTeamDocsDir 安全日产物目录改名级联（Node safeday/migrate.renameTeamFolder 的 docs 部分）：
// docs/{旧名} → docs/{新名}；目标已存在则逐个移入（同名文件跳过、源保留），失败仅告警不阻断。
func renameTeamDocsDir(docsDir, oldName, newName string) {
	oldDir := filepath.Join(docsDir, oldName)
	newDir := filepath.Join(docsDir, newName)
	if info, err := os.Stat(oldDir); err != nil || !info.IsDir() {
		return
	}
	if _, err := os.Stat(newDir); err != nil {
		if err := os.Rename(oldDir, newDir); err != nil {
			log.Printf("[安全日] 班组目录改名失败（请手工调整 docs 目录）：%v", err)
		}
		return
	}
	entries, err := os.ReadDir(oldDir)
	if err != nil {
		log.Printf("[安全日] 旧目录读取失败（保留）%s：%v", oldDir, err)
		return
	}
	for _, e := range entries {
		src := filepath.Join(oldDir, e.Name())
		dst := filepath.Join(newDir, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue // 目标已存在则跳过，源保留（Node moveFile 同口径）
		}
		if err := os.Rename(src, dst); err != nil {
			log.Printf("[安全日] 文件迁移失败 %s → %s：%v", src, dst, err)
		}
	}
	if err := os.Remove(oldDir); err != nil {
		log.Printf("[安全日] 旧目录删除失败（保留）%s：%v", oldDir, err)
	}
}
