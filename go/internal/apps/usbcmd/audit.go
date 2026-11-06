package usbcmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// AuditSink 指令/结果审计落库抽象（测试可注入 fake；nil 时跳过审计）。
type AuditSink interface {
	Record(ctx context.Context, res Result) error
	Timeout(ctx context.Context, app, id, errText string) error
}

// Audit 进程级审计器（main 启动时 SetAudit；nil 不落库，不影响同步）。
var Audit AuditSink

// SetAudit 装配审计器（测试可注入 fake）。
func SetAudit(s AuditSink) { Audit = s }

const auditDDL = `CREATE TABLE IF NOT EXISTS intranet_sync_cmd (
  id VARCHAR(128) NOT NULL PRIMARY KEY,
  app VARCHAR(32) NOT NULL,
  action VARCHAR(32) NOT NULL DEFAULT '',
  issued_at DATETIME NULL,
  finished_at DATETIME NULL,
  ok TINYINT NOT NULL DEFAULT 0,
  duration_ms BIGINT NOT NULL DEFAULT 0,
  error TEXT,
  client_host VARCHAR(128) NOT NULL DEFAULT '',
  client_version VARCHAR(64) NOT NULL DEFAULT '',
  target_node VARCHAR(64) NOT NULL DEFAULT '',
  exec_node VARCHAR(64) NOT NULL DEFAULT '',
  relayed_by VARCHAR(64) NOT NULL DEFAULT '',
  result_json MEDIUMTEXT,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  KEY idx_app_created (app, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='内网 U 盘指令/结果审计（默认 30 天）'`

// AuditStore MySQL 审计实现；RetentionDays<=0 时清理不生效。
type AuditStore struct {
	DB            *sql.DB
	RetentionDays int
}

// EnsureSchema 建审计表（幂等）。
func (s *AuditStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return nil
	}
	if _, err := s.DB.ExecContext(ctx, auditDDL); err != nil {
		return err
	}
	// 老库补列（MySQL 无 ADD COLUMN IF NOT EXISTS；已存在时 1060 忽略）。
	for _, ddl := range []string{
		`ALTER TABLE intranet_sync_cmd ADD COLUMN target_node VARCHAR(64) NOT NULL DEFAULT ''`,
		`ALTER TABLE intranet_sync_cmd ADD COLUMN exec_node VARCHAR(64) NOT NULL DEFAULT ''`,
		`ALTER TABLE intranet_sync_cmd ADD COLUMN relayed_by VARCHAR(64) NOT NULL DEFAULT ''`,
	} {
		_, _ = s.DB.ExecContext(ctx, ddl)
	}
	return nil
}

// Record 写入/覆盖一条结果（按 id 幂等）。
func (s *AuditStore) Record(ctx context.Context, res Result) error {
	if s == nil || s.DB == nil {
		return nil
	}
	host, version := "", ""
	if v, ok := res.Client["host"]; ok {
		host = fmt.Sprint(v)
	}
	if v, ok := res.Client["version"]; ok {
		version = fmt.Sprint(v)
	}
	raw, _ := json.Marshal(res)
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO intranet_sync_cmd
		   (id, app, action, issued_at, finished_at, ok, duration_ms, error, client_host,
		    client_version, target_node, exec_node, relayed_by, result_json)
		 VALUES (?, ?, ?, NOW(), NOW(), ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   finished_at=VALUES(finished_at), ok=VALUES(ok), duration_ms=VALUES(duration_ms),
		   error=VALUES(error), client_host=VALUES(client_host), client_version=VALUES(client_version),
		   target_node=VALUES(target_node), exec_node=VALUES(exec_node),
		   relayed_by=VALUES(relayed_by), result_json=VALUES(result_json)`,
		res.ID, res.App, res.Action, res.OK, res.DurationMS, res.Error, host, version,
		res.Target, res.Node, res.RelayedBy, string(raw))
	return err
}

// Timeout 记录一次服务端等待超时（无客户端结果）。
func (s *AuditStore) Timeout(ctx context.Context, app, id, errText string) error {
	if s == nil || s.DB == nil {
		return nil
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO intranet_sync_cmd (id, app, action, issued_at, finished_at, ok, error)
		 VALUES (?, ?, '', NOW(), NOW(), 0, ?)
		 ON DUPLICATE KEY UPDATE finished_at=VALUES(finished_at), ok=0, error=VALUES(error)`,
		id, app, errText)
	return err
}

// Cleanup 删除超过 RetentionDays 的审计行（<=0 不清理）。
func (s *AuditStore) Cleanup(ctx context.Context) error {
	if s == nil || s.DB == nil || s.RetentionDays <= 0 {
		return nil
	}
	_, err := s.DB.ExecContext(ctx,
		`DELETE FROM intranet_sync_cmd WHERE created_at < DATE_SUB(NOW(), INTERVAL ? DAY)`,
		s.RetentionDays)
	return err
}

// StartAuditCleanup 启动每日审计清理（启动即跑一次）；返回停止函数（幂等）。
func StartAuditCleanup(ctx context.Context, store *AuditStore) (stop func()) {
	if store == nil {
		return func() {}
	}
	stopCh := make(chan struct{})
	go func() {
		run := func() {
			cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := store.Cleanup(cctx); err != nil {
				log.Printf("[usbcmd] 审计清理失败: %v", err)
			}
		}
		run()
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				run()
			case <-stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stopCh) })
	}
}

// recordAudit 同步主流程的 best-effort 审计：短超时，失败只记日志。
func recordAudit(res Result) {
	if Audit == nil || res.ID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Audit.Record(ctx, res); err != nil {
		log.Printf("[usbcmd] 审计写入失败 %s: %v", res.ID, err)
	}
}

func recordAuditTimeout(app, id, errText string) {
	if Audit == nil || id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Audit.Timeout(ctx, app, id, errText); err != nil {
		log.Printf("[usbcmd] 审计超时写入失败 %s: %v", id, err)
	}
}
