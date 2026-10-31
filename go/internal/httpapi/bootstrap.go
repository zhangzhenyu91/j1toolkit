package httpapi

// 启动种子（幂等、不破坏旧行）：sys_app 新应用 sort12/13 补缺 + 账号核心自举（班组/初始管理员）。
// 对齐 server/src/db.js 的账号相关种子；仅补缺不覆盖既有数据（已有行一律不动）。

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"golang.org/x/crypto/bcrypt"
)

// defaultAdminPassword ADMIN_PASSWORD 缺省值（同 Node config.js；使用时醒目告警）。
const defaultAdminPassword = "Admin@123"

// 基础应用种子（五件套 + 五个业务应用，同 server/src/db.js；INSERT IGNORE 只补缺，
// 不批量改名、不覆盖既有行）。业务应用必须在此种子：否则空库首启时 BootstrapAccounts
// 先于各模块建表，管理员默认授权查不到 app_key 会静默跳过。
const seedAppsSQL = `
INSERT IGNORE INTO sys_app (app_key, name, icon, path, terminal, sort, status) VALUES
  ('call-me', 'Call Me', 'robot', '/pkg-callme/pages/sessions/sessions', 'both', 1, 1),
  ('netdisk', '团队网盘', 'folder-open', '/pkg-netdisk/pages/index/index', 'both', 2, 1),
  ('work-log', '出工日志', 'calendar', '/pkg-worklog/pages/index/index', 'both', 3, 1),
  ('tree-hazard', '树障隐患', 'forest', '/pkg-treehazard/pages/index/index', 'both', 4, 1),
  ('crossing', '交跨记录', 'fork', '/pkg-crossing/pages/index/index', 'both', 5, 1),
  ('file-transfer', '文件传输', 'swap', '/pkg-filetransfer/pages/index/index', 'mobile', 6, 1),
  ('kvm', '远程控制', 'terminal', '', 'pc', 7, 1),
  ('wm-add', '水印添加', 'image', '/pkg-wmadd/pages/index/index', 'both', 9, 1),
  ('safe-day', '安全日活动', 'file-safety', '/pkg-safeday/pages/index/index', 'both', 10, 1),
  ('team-data', '班组数据', 'chart-bar', '/pkg-teamdata/pages/index/index', 'both', 11, 1)
`

// 管理应用种子（新应用，sort 12/13；INSERT IGNORE 不覆盖已有行）。
// wechat-answer 微信智能应答 / app-log 应用日志：管理端应用，app/list 对 admin 恒可见；
// path 取 Web 端路由 /wechat-answer 与 /app-logs。
const seedAppExtrasSQL = `
INSERT IGNORE INTO sys_app (app_key, name, icon, path, terminal, sort, status) VALUES
  ('wechat-answer', '微信智能应答', 'chat', '/wechat-answer', 'pc', 12, 1),
  ('app-log', '应用日志', 'file-logs', '/app-logs', 'pc', 13, 1)
`

// SeedAppExtras 补 sys_app 应用种子（原有五件套 + wechat-answer sort 12 / app-log sort 13），
// 全部 INSERT IGNORE 只补缺：保留用户与应用历史，不覆盖既有行、不动既有授权。
func SeedAppExtras(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, seedAppsSQL); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, seedAppExtrasSQL); err != nil {
		return err
	}
	// 仅迁移明确更名的旧显示名，不覆盖用户自定义名称。
	_, err := db.ExecContext(ctx, `UPDATE sys_app SET name = CASE app_key
		WHEN 'safe-day' THEN '安全日活动'
		WHEN 'kvm' THEN '远程控制'
		ELSE name END
	WHERE (app_key = 'safe-day' AND name IN ('安全日', '安全日活动记录'))
		OR (app_key = 'kvm' AND name IN ('KVM 远程', '远程连接计算机'))`)
	return err
}

// BootstrapAccounts 账号核心自举（全部幂等、只补缺，保留用户与应用历史）：
//  1. sys_team 空表时写入班组种子（检修一班 = 默认班组，sort 最小）；
//  2. 初始管理员不存在时创建（bcrypt，用户名/密码取 ADMIN_USERNAME / ADMIN_PASSWORD）；
//  3. 环境变量指定管理员始终具备 admin 角色（防误改，同 Node）；
//  4. 老库 sys_user.team 文本班名并入 sys_team（只补缺、不改名）；
//  5. 老库 sys_user.team 文本回填 sys_user.team_id（不丢老数据班名）；
//  6. 管理员默认授予 call-me / safe-day / kvm / file-transfer / wm-add 应用权限。
//
// 只升不降：启动自举只提升 ENV 管理员为 admin，绝不降级/删除任何账号，
// 「不能删除/降级最后一个超管」约束（adminapi）不被启动破坏。
func BootstrapAccounts(ctx context.Context, db *sql.DB, adminUsername, adminPassword string) error {
	if adminUsername == "" {
		adminUsername = "admin"
	}
	if adminPassword == "" {
		adminPassword = defaultAdminPassword
		// 默认密码缺失醒目告警（同 Node config.js validateConfig 口径，仅提示不改默认行为）
		log.Printf("************************************************************")
		log.Printf("[安全警告] 未配置 ADMIN_PASSWORD，初始管理员使用默认密码「%s」，", defaultAdminPassword)
		log.Printf("[安全警告] 存在被猜测/撞库风险，请尽快在 .env 中设置强密码！")
		log.Printf("************************************************************")
	}
	// 1. 班组种子（仅空表）
	var teamCnt int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sys_team`).Scan(&teamCnt); err != nil {
		return err
	}
	if teamCnt == 0 {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO sys_team (name, sort) VALUES (?, 1), (?, 2)`, "检修一班", "运维二班"); err != nil {
			return err
		}
	}
	// 2. 初始管理员（仅账号不存在时创建）
	var adminID int64
	err := db.QueryRowContext(ctx, `SELECT id FROM sys_user WHERE username = ?`, adminUsername).Scan(&adminID)
	if errors.Is(err, sql.ErrNoRows) {
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
		if hashErr != nil {
			return hashErr
		}
		res, execErr := db.ExecContext(ctx,
			`INSERT INTO sys_user (username, password_hash, nickname, team, role) VALUES (?, ?, ?, ?, 'admin')`,
			adminUsername, string(hash), adminUsername, "检修一班")
		if execErr != nil {
			return execErr
		}
		adminID, _ = res.LastInsertId()
	} else if err != nil {
		return err
	}
	// 3. 管理员角色兜底（同 Node：环境变量指定管理员始终 admin）
	if _, err := db.ExecContext(ctx,
		`UPDATE sys_user SET role = 'admin' WHERE username = ?`, adminUsername); err != nil {
		return err
	}
	// 4. 老库 sys_user.team 文本班名并入 sys_team（只补缺、不改名；sort=100 排在种子班组后）
	if _, err := db.ExecContext(ctx,
		`INSERT IGNORE INTO sys_team (name, sort)
		 SELECT DISTINCT u.team, 100 FROM sys_user u
		 WHERE u.team <> '' AND u.team NOT IN (SELECT name FROM sys_team)`); err != nil {
		return err
	}
	// 5. 老库 team 文本回填 team_id（空 team 保持 NULL = 未分配班组）
	if _, err := db.ExecContext(ctx,
		`UPDATE sys_user u JOIN sys_team t ON t.name = u.team
		 SET u.team_id = t.id WHERE u.team_id IS NULL AND u.team <> ''`); err != nil {
		return err
	}
	// 6. 管理员默认应用授权（五件套 + 五个业务应用；INSERT IGNORE 不覆盖既有授权）
	if adminID > 0 {
		for _, key := range []string{"call-me", "safe-day", "kvm", "file-transfer", "wm-add",
			"netdisk", "work-log", "tree-hazard", "crossing", "team-data"} {
			if _, err := db.ExecContext(ctx,
				`INSERT IGNORE INTO sys_user_app (user_id, app_id) SELECT ?, id FROM sys_app WHERE app_key = ?`,
				adminID, key); err != nil {
				return err
			}
		}
	}
	return nil
}
