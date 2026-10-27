// 成员显示名解析：sys_user.wxid 唯一启用账号优先（nickname 回退 username），
// 其次 WeChatPadPro 群成员接口（群昵称>微信昵称），最后 wxid 兜底。
package wechatanswer

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"
)

// SQLNameResolver 默认实现（Members 未注入时仅走 sys_user + wxid 兜底）。
type SQLNameResolver struct {
	DB      *sql.DB
	Members MemberSource
	// TTL 群成员缓存时长；0 = 10 分钟。
	TTL time.Duration

	mu    sync.Mutex
	cache map[string]memberCacheEntry
}

var _ NameResolver = (*SQLNameResolver)(nil)

type memberCacheEntry struct {
	fetchedAt time.Time
	members   map[string]string
}

func (r *SQLNameResolver) ttl() time.Duration {
	if r.TTL > 0 {
		return r.TTL
	}
	return 10 * time.Minute
}

// userByWxid wxid → 唯一启用账号显示名（0 命中或多绑 → sql.ErrNoRows / 错误）。
func (r *SQLNameResolver) userByWxid(ctx context.Context, wxid string) (string, error) {
	if r.DB == nil || wxid == "" {
		return "", sql.ErrNoRows
	}
	rows, err := r.DB.QueryContext(ctx,
		`SELECT nickname, username FROM sys_user WHERE wxid = ? AND status = 1`, wxid)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var nickname, username string
		if err := rows.Scan(&nickname, &username); err != nil {
			return "", err
		}
		name := strings.TrimSpace(nickname)
		if name == "" {
			name = strings.TrimSpace(username)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(names) != 1 {
		return "", sql.ErrNoRows // 0 命中或多绑：不作数，走下一优先级
	}
	return names[0], nil
}

// groupMembers 群成员（带缓存）。
func (r *SQLNameResolver) groupMembers(ctx context.Context, groupID string) (map[string]string, error) {
	if r.Members == nil {
		return nil, errors.New("未接入群成员接口")
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]memberCacheEntry{}
	}
	if e, ok := r.cache[groupID]; ok && time.Since(e.fetchedAt) < r.ttl() {
		r.mu.Unlock()
		return e.members, nil
	}
	r.mu.Unlock()
	members, err := r.Members.GroupMembers(ctx, groupID)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cache[groupID] = memberCacheEntry{fetchedAt: time.Now(), members: members}
	r.mu.Unlock()
	return members, nil
}

// DisplayName 解析发送者显示名。
func (r *SQLNameResolver) DisplayName(ctx context.Context, groupID, wxid string) (string, error) {
	if wxid == "" {
		return "", nil
	}
	if name, err := r.userByWxid(ctx, wxid); err == nil && name != "" {
		return name, nil
	}
	members, err := r.groupMembers(ctx, groupID)
	if err == nil {
		if name := members[wxid]; name != "" {
			return name, nil
		}
	}
	return wxid, nil
}

// WxidByName 反解析：显示名 → wxid（sys_user 唯一启用账号优先，群成员接口兜底）。
func (r *SQLNameResolver) WxidByName(ctx context.Context, groupID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if r.DB != nil {
		rows, err := r.DB.QueryContext(ctx,
			`SELECT wxid FROM sys_user WHERE status = 1 AND wxid IS NOT NULL AND wxid<>'' AND (nickname = ? OR username = ?)`,
			name, name)
		if err == nil {
			defer rows.Close()
			var wxids []string
			for rows.Next() {
				var wxid string
				if rows.Scan(&wxid) == nil && wxid != "" {
					wxids = append(wxids, wxid)
				}
			}
			if len(wxids) == 1 {
				return wxids[0], nil
			}
		}
	}
	members, err := r.groupMembers(ctx, groupID)
	if err != nil {
		return "", err
	}
	for wxid, display := range members {
		if display == name {
			return wxid, nil
		}
	}
	return "", nil
}

// names 取显示名解析器（注入优先；缺省 SQL + PadPro 群成员）。
func (m *Module) names() NameResolver {
	if m.Names != nil {
		return m.Names
	}
	return &SQLNameResolver{DB: m.DB, Members: m.Members}
}

// rulesProvider 取规则来源（注入优先；缺省 MySQL）。
func (m *Module) rulesProvider() RuleProvider {
	if m.Rules != nil {
		return m.Rules
	}
	return SQLRuleProvider{DB: m.DB}
}

// recordStore 取管线状态存储（注入优先；缺省 MySQL）。
func (m *Module) recordStore() RecordStore {
	if m.State != nil {
		return m.State
	}
	return &SQLRecordStore{DB: m.DB}
}

// pushQueue 取补推队列（注入优先；缺省 MySQL）。
func (m *Module) pushQueue() PushQueue {
	if m.Queue != nil {
		return m.Queue
	}
	return &SQLPushQueue{DB: m.DB}
}
