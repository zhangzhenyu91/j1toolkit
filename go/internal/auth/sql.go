package auth

import (
	"context"
	"database/sql"
	"time"

	"github.com/redis/go-redis/v9"
)

// SQLUserStore 从 sys_user 读取账号；禁用账号按不存在处理。
type SQLUserStore struct{ DB *sql.DB }

func (s SQLUserStore) ByUsername(ctx context.Context, username string) (Identity, string, error) {
	var u Identity
	var hash string
	var team sql.NullInt64
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, username, nickname, role, team_id, password_hash FROM sys_user WHERE username = ? AND status = 1`, username).
		Scan(&u.ID, &u.Username, &u.Nickname, &u.Role, &team, &hash)
	u.TeamID = team.Int64
	return u, hash, err
}

func (s SQLUserStore) ByID(ctx context.Context, id int64) (Identity, error) {
	var u Identity
	var team sql.NullInt64
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, username, nickname, role, team_id FROM sys_user WHERE id = ? AND status = 1`, id).
		Scan(&u.ID, &u.Username, &u.Nickname, &u.Role, &team)
	u.TeamID = team.Int64
	return u, err
}

// RedisBlacklist 使用与现有 Node 相同的 JWT 黑名单键前缀。
type RedisBlacklist struct{ Client *redis.Client }

func (b RedisBlacklist) Add(ctx context.Context, token string, ttl time.Duration) error {
	return b.Client.Set(ctx, "jwt:blacklist:"+token, "1", ttl).Err()
}

func (b RedisBlacklist) Contains(ctx context.Context, token string) (bool, error) {
	value, err := b.Client.Get(ctx, "jwt:blacklist:"+token).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return value == "1", nil
}
