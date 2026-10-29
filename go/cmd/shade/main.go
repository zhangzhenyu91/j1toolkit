// Shade 壹匣 Go 服务装配入口：统一登录 + 全业务模块挂载 + 后台排程启停。
// 生产启动顺序：MySQL/Redis → 系统表（sys_team/sys_user/sys_app/sys_user_app 建表与老库补列）→
// 运行配置（sys_runtime_config / app_log）→ 首次 env 补缺（含 sgccclockin.ConfigSpecs 合并）→
// 通知表/sys_app 应用种子/账号自举 → 各模块建表（依赖序：worklog 先于 teamdata，商旅表依赖
// worklog_photo 全列）→ 出工成员启动补缺 → 路由装配 → 排程启动。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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

	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func mysqlDSN() string {
	if dsn := os.Getenv("MYSQL_DSN"); dsn != "" {
		return dsn
	}
	user := os.Getenv("MYSQL_USER")
	password := os.Getenv("MYSQL_PASSWORD")
	host := env("MYSQL_HOST", "127.0.0.1")
	port := env("MYSQL_PORT", "3306")
	database := os.Getenv("MYSQL_DATABASE")
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&charset=utf8mb4&loc=Local", user, password, host, port, database)
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
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("MySQL 连接失败: %w", err)
	}
	return db, nil
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

// wechatPermit 微信智能应答为仅超管开放的管理应用；
// 其余应用键原样走 sys_user_app。
type wechatPermit struct{ db *sql.DB }

func (p wechatPermit) Allow(ctx context.Context, userID int64, appKey string) (bool, error) {
	if appKey == wechatanswer.AppKey {
		var role string
		err := p.db.QueryRowContext(ctx, `SELECT role FROM sys_user WHERE id = ? AND status = 1`, userID).Scan(&role)
		if err == nil && role == "admin" {
			return true, nil
		}
		appKey = "wechat-answer"
	}
	return httpapi.SQLAppPermit{DB: p.db}.Allow(ctx, userID, appKey)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	var deps httpapi.Deps
	var db *sql.DB
	var modules []httpapi.Module
	var cleanups []func()
	var cfgStore httpapi.ConfigValue
	if os.Getenv("SHADE_LOCAL_FIXTURE") == "1" {
		deps = httpapi.FixtureDeps()
		deps.Auth = auth.FixtureAuth{}
		log.Printf("[启动] 使用本地 fixture，仅用于开发验证，不连接 MySQL")
	} else {
		var err error
		db, err = openDatabase()
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
		jwtSecret := os.Getenv("JWT_SECRET")
		if jwtSecret == "" {
			log.Fatal("JWT_SECRET 未配置")
		}
		redisClient := redis.NewClient(&redis.Options{
			Addr:     fmt.Sprintf("%s:%s", env("REDIS_HOST", "127.0.0.1"), env("REDIS_PORT", "6379")),
			Password: os.Getenv("REDIS_PASSWORD"),
		})
		defer redisClient.Close()
		cipher, err := config.NewConfigCipher(os.Getenv("CONFIG_MASTER_KEY"), jwtSecret)
		if err != nil {
			log.Fatal(err)
		}
		store := config.NewStore(db, cipher)
		cfgStore = store
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// schema 依赖顺序：系统表（空库建表/老库补列）→ 配置/日志/通知表 → 应用种子 → 账号自举
		// → 各业务模块（worklog 先于 teamdata/sgcc）
		if err := httpapi.EnsureSystemSchema(ctx, db); err != nil {
			log.Fatal(err)
		}
		if err := store.EnsureSchema(ctx); err != nil {
			log.Fatal(err)
		}
		if err := config.EnsureAppLogSchema(ctx, db); err != nil {
			log.Fatal(err)
		}
		if err := httpapi.EnsureNoticeSchema(ctx, db); err != nil {
			log.Fatal(err)
		}
		// 应用种子先于账号自举：管理员默认授权按 app_key 行补缺，种子不在位会静默跳过
		if err := httpapi.SeedAppExtras(ctx, db); err != nil {
			log.Fatal(err)
		}
		if err := httpapi.BootstrapAccounts(ctx, db, os.Getenv("ADMIN_USERNAME"), os.Getenv("ADMIN_PASSWORD")); err != nil {
			log.Fatal(err)
		}
		// 首次 env 补缺：注册表合并 sgccclockin.ConfigSpecs（同键模块规格优先）
		specs := config.MergeSpecs(config.DefaultSpecs(), sgccclockin.ConfigSpecs())
		seeded, err := store.SeedFromEnv(ctx, specs, os.Getenv)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("[启动] 运行配置初始化完成，首次补缺 %d 项", seeded)

		authService := auth.NewService(auth.SQLUserStore{DB: db}, auth.RedisBlacklist{Client: redisClient}, jwtSecret, 0, 0)
		permit := httpapi.SQLAppPermit{DB: db}
		// 系统公共事件与访问日志归 system，具体应用事件使用各自的 sys_app 键。
		appLog := logging.New(db, "system")
		provider := integrations.NewRuntimeProvider(store)
		cosStore := &integrations.COSStore{Cfg: provider, Recorder: logging.New(db, "system")}
		amap := &integrations.AMapService{Cfg: provider, Recorder: logging.New(db, "work-log")}
		worklogVerifier := integrations.NewPhotoVerifier(&integrations.WorkflowClient{Cfg: provider, APIKeyKey: "DIFY_WORKLOG_API_KEY", Recorder: logging.New(db, "work-log")})
		safedayRunner := &integrations.StreamRunner{Client: &integrations.WorkflowClient{Cfg: provider, APIKeyKey: "DIFY_SAFEDAY_API_KEY", Recorder: logging.New(db, "safe-day")}}
		weknora := callme.NewHTTPClient(store)
		// WeChatPadPro 云端网关客户端（bridge 集中构造）：商旅通知微信侧用；wechatanswer
		// 五通道由 bridge.Wire 内部另建实例注入（客户端无会话状态，多例等价）
		padPro := bridge.NewPadPro(store)

		deps = httpapi.Deps{
			Apps:     httpapi.SQLAppCatalog{DB: db},
			Cfg:      httpapi.StoreConfigAdmin{Store: store},
			Logs:     httpapi.SQLLogReader{DB: db},
			Auth:     authService,
			Accounts: httpapi.SQLAccountStore{DB: db},
			Notices:  httpapi.SQLNoticeReader{DB: db},
		}

		// ---- 业务模块（构造不分先后，建表按依赖序执行）----
		// 商旅打卡（出工日志扩展；任务队列供 worklog 复用）
		sgc := sgccclockin.New(db)
		sgc.Permit = permit
		sgc.Cfg = store
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

		// ONLYOFFICE（Sources / NetdiskDoc 由下方 bridge.Wire 注入，缺模块时对应源报未接入）
		of := office.New(store)
		of.Permit = permit

		// 题库刷题
		qz := quiz.New(db)
		qz.Permit = permit
		qz.Cfg = store
		qz.Log = logging.New(db, "quiz")
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
			log.Fatal(err)
		}
		sd := safeday.New(db, rs)
		sd.Permit = permit
		sd.Cfg = store
		sd.Auth = authService
		sd.Log = logging.New(db, "safe-day")
		sd.Dify = safedayRunner
		sd.Converter = &safeday.LibreOfficeConverter{SofficePath: cfgOrEnv(ctx, store, "SAFEDAY_SOFFICE_PATH")}
		sd.COS = cosStore

		wxAuth := authwx.New(db, store, authService)
		admin := adminapi.New(db)
		admin.Log = logging.New(db, "system")
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
		wa.AppLog = logging.New(db, "wechat-answer")
		store.OnChange(func(ev config.ChangeEvent) {
			switch ev.Key {
			case "WECHATPADPRO_TOKEN", "WECHATPADPRO_API_BASE", "WECHATPADPRO_WS_URL", "WECHATPADPRO_TICKET_PATH", "WECHATPADPRO_BOT_WXID", "WECHATPADPRO_FILE_APP_ID":
				wa.Reconnect(context.Background())
			}
		})
		// Sender/Members/Images/Files/Sync 五通道与 Ask/Chatlog 直调出口由下方 bridge.Wire 集中注入

		// ---- 跨模块桥（go/internal/apps/bridge）：office 五源/网盘文档源、worklog/safeday
		// 网盘保存、kvm 网盘互推、WF3 命名、微信智能应答五通道/提问/归档、tree/crossing Usb
		// 与结果通知、worklog 人工审核通知与派车单取件建卡、捕获出口 ----
		wa.Sink = bridge.Wire(bridge.Deps{
			Office: of, Worklog: wl, Safeday: sd, Tree: th, Crossing: cr, Kvm: kv, Netdisk: nd,
			Callme: cm, Chatlog: cl, Wechat: wa,
			Cfg: store, DB: db, Platform: glkvmClient, Locker: kv.Locker,
			AdminUsername: env("ADMIN_USERNAME", "admin"),
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
				log.Fatalf("[启动] %s 建表失败: %v", ensure.name, err)
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
				Logger: logging.New(db, "tree-hazard"),
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
				Logger: logging.New(db, "crossing"),
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
			disp := dispatchPair{Logger: logging.New(db, "work-log"), Prepare: jobs.Prepare, Fetch: jobs.Fetch}
			cleanups = append(cleanups, scheduleDailyCn(
				func(ctx context.Context) string { return cfgOrEnv(ctx, store, "WORKLOG_DISPATCH_PREPARE_TIME") },
				9, 10, disp.RunPrepare))
			cleanups = append(cleanups, scheduleDailyCn(
				func(ctx context.Context) string { return cfgOrEnv(ctx, store, "WORKLOG_DISPATCH_FETCH_TIME") },
				9, 20, disp.RunFetch))
		}

		// ---- 应用日志 7 天清理（启动即清一次，此后每 24h）----
		retentionStop := make(chan struct{})
		cleanups = append(cleanups, func() { close(retentionStop) })
		go func() {
			run := func() {
				cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
				defer ccancel()
				n, err := config.CleanupAppLog(cctx, db, config.AppLogRetentionDays)
				if err != nil {
					log.Printf("[日志清理] 失败: %v", err)
					return
				}
				log.Printf("[日志清理] 七天前日志清理完成，删除 %d 条", n)
				_ = appLog.Info(cctx, "scheduler", "log.retention.completed",
					"七天前日志清理完成", fmt.Sprintf("[app-log] retention cleanup completed: %d rows", n),
					map[string]any{"deleted": n})
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

		_ = appLog.Info(context.Background(), "business", "runtime.started", "Go 服务启动完成", "shade go service started", nil)
	}

	port, err := strconv.Atoi(env("PORT", "3000"))
	if err != nil || port < 1 || port > 65535 {
		log.Fatalf("PORT 必须是 1-65535 的整数: %v", err)
	}
	handler := httpapi.WithAccessLog(httpapi.WithLegacyPages(httpapi.WithSPA(httpapi.NewRouter(deps, modules...), env("SHADE_WEB_DIR", "../web/dist")), env("SHADE_LEGACY_PUBLIC_DIR", "../server/public"), cfgStore), db)
	// SSE 与长任务不设总写超时；慢速大图上传不设总读取超时，各接口以严格字节上限兜底。
	server := &http.Server{Addr: fmt.Sprintf("0.0.0.0:%d", port), Handler: handler, ReadHeaderTimeout: 15 * time.Second, ReadTimeout: 0, WriteTimeout: 0, IdleTimeout: 120 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("[启动] Shade Go 服务监听 %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
	log.Printf("[关闭] Shade Go 服务已退出")
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
