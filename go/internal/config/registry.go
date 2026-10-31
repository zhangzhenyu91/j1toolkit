package config

import "strings"

// DefaultSpecs 返回运行配置注册表。启动项仍由 env 提供，表中仅包含可迁入库的运行配置。
func DefaultSpecs() []Spec {
	return []Spec{
		{Key: "DIFY_API_URL", Env: "DIFY_API_URL"},
		{Key: "DIFY_USER", Env: "DIFY_USER", Default: "shade-yixia"},
		{Key: "DIFY_TIMEOUT_MS", Env: "DIFY_TIMEOUT_MS", Default: "90000"},
		{Key: "DIFY_WORKLOG_API_KEY", Env: "DIFY_WORKLOG_API_KEY", Sensitive: true},
		{Key: "DIFY_SAFEDAY_API_KEY", Env: "DIFY_SAFEDAY_API_KEY", Sensitive: true},
		{Key: "DIFY_QUIZ_API_KEY", Env: "DIFY_QUIZ_API_KEY", Sensitive: true},
		{Key: "DIFY_WF3_API_KEY", Env: "DIFY_WF3_API_KEY", Sensitive: true},
		{Key: "DIFY_WF4_API_KEY", Env: "DIFY_WF4_API_KEY", Sensitive: true},
		{Key: "WECHATPADPRO_TOKEN", Env: "WECHATPADPRO_TOKEN", Sensitive: true},
		{Key: "WECHATPADPRO_API_BASE", Env: "WECHATPADPRO_API_BASE", Default: "https://api.wechatpadpro.com"},
		{Key: "WECHATPADPRO_TICKET_PATH", Env: "WECHATPADPRO_TICKET_PATH", Default: "/cloud/v1/Runtime/WebSocketTicket"},
		{Key: "WECHATPADPRO_WS_URL", Env: "WECHATPADPRO_WS_URL", Default: "wss://api.wechatpadpro.com/ws/sync"},
		{Key: "WECHATPADPRO_BOT_WXID", Env: "WECHATPADPRO_BOT_WXID"},
		{Key: "WECHATPADPRO_FILE_APP_ID", Env: "WECHATPADPRO_FILE_APP_ID", Default: "ID_10001"},
		{Key: "LOG_RAW_MESSAGE", Env: "LOG_RAW_MESSAGE", Default: "0"},
		{Key: "APP_LOG_RETENTION_DAYS", Env: "APP_LOG_RETENTION_DAYS", Default: "0"},
		{Key: "APP_LOG_ARCHIVE_RETENTION_DAYS", Env: "APP_LOG_ARCHIVE_RETENTION_DAYS", Default: "0"},
		{Key: "CHATLOG_DIGEST_TIME", Env: "CHATLOG_DIGEST_TIME", Default: "23:26"},
		{Key: "WEKNORA_API_URL", Env: "WEKNORA_API_URL", Default: "https://know.j1net.com/api/v1"},
		{Key: "WEKNORA_API_KEY", Env: "WEKNORA_API_KEY", Sensitive: true},
		{Key: "WEKNORA_AGENT_ID", Env: "WEKNORA_AGENT_ID"},
		{Key: "WEKNORA_SYNC_USER", Env: "WEKNORA_SYNC_USER", Default: "admin"},
		{Key: "WEKNORA_CHATLOG_KB_ID", Env: "WEKNORA_CHATLOG_KB_ID"},
		{Key: "WEKNORA_TREEHAZARD_KB_ID", Env: "WEKNORA_TREEHAZARD_KB_ID"},
		{Key: "WEKNORA_CROSSING_KB_ID", Env: "WEKNORA_CROSSING_KB_ID"},
		{Key: "CALLME_INGEST_TOKEN", Env: "CALLME_INGEST_TOKEN", Sensitive: true},
		{Key: "CHATLOG_INGEST_TOKEN", Env: "CHATLOG_INGEST_TOKEN", Sensitive: true},
		{Key: "TREEHAZARD_INGEST_TOKEN", Env: "TREEHAZARD_INGEST_TOKEN", Sensitive: true},
		{Key: "CROSSING_INGEST_TOKEN", Env: "CROSSING_INGEST_TOKEN", Sensitive: true},
		{Key: "COS_SECRET_ID", Env: "COS_SECRET_ID", Sensitive: true},
		{Key: "COS_SECRET_KEY", Env: "COS_SECRET_KEY", Sensitive: true},
		{Key: "COS_BUCKET", Env: "COS_BUCKET"},
		{Key: "COS_REGION", Env: "COS_REGION"},
		{Key: "COS_WORKLOG_PREFIX", Env: "COS_WORKLOG_PREFIX", Default: "worklog/"},
		{Key: "COS_WORKLOG_BASE_URL", Env: "COS_WORKLOG_BASE_URL"},
		{Key: "COS_TREEHAZARD_PREFIX", Env: "COS_TREEHAZARD_PREFIX", Default: "treehazard/"},
		{Key: "AMAP_MAP_KEY", Env: "AMAP_MAP_KEY", Sensitive: true},
		{Key: "AMAP_BASE_URL", Env: "AMAP_BASE_URL", Default: "https://restapi.amap.com"},
		{Key: "GLKVM_URL", Env: "GLKVM_URL"},
		{Key: "GLKVM_PASSWORD", Env: "GLKVM_PASSWORD", Sensitive: true},
		{Key: "NETDISK_API_URL", Env: "NETDISK_API_URL", Default: "http://127.0.0.1:5244"},
		{Key: "NETDISK_USERNAME", Env: "NETDISK_USERNAME"},
		{Key: "NETDISK_PASSWORD", Env: "NETDISK_PASSWORD", Sensitive: true},
		{Key: "NETDISK_PUBLIC_ROOT", Env: "NETDISK_PUBLIC_ROOT", Default: "/public"},
		{Key: "NETDISK_PERSONAL_ROOT", Env: "NETDISK_PERSONAL_ROOT", Default: "/personal"},
		{Key: "NETDISK_MAX_UPLOAD_MB", Env: "NETDISK_MAX_UPLOAD_MB", Default: "1024"},
		{Key: "NETDISK_THUMB_DIR", Env: "NETDISK_THUMB_DIR", Default: "./data/thumbs"},
		{Key: "NETDISK_THUMB_MAX_MB", Env: "NETDISK_THUMB_MAX_MB", Default: "50"},
		{Key: "OO_PUBLIC_URL", Env: "OO_PUBLIC_URL"},
		{Key: "OO_INTERNAL_URL", Env: "OO_INTERNAL_URL", Default: "http://onlyoffice"},
		{Key: "OO_CALLBACK_BASE", Env: "OO_CALLBACK_BASE"},
		{Key: "OO_JWT_SECRET", Env: "OO_JWT_SECRET", Sensitive: true},
		{Key: "OO_TICKET_SECRET", Env: "OO_TICKET_SECRET", Sensitive: true},
		{Key: "OO_FILE_TICKET_TTL", Env: "OO_FILE_TICKET_TTL", Default: "600"},
		{Key: "OO_CALLBACK_TICKET_TTL", Env: "OO_CALLBACK_TICKET_TTL", Default: "43200"},
		{Key: "OO_SAVE_TEMP_DIR", Env: "OO_SAVE_TEMP_DIR"},
		{Key: "SAFEDAY_DATA_DIR", Env: "SAFEDAY_DATA_DIR", Default: "./data/safeday"},
		{Key: "SAFEDAY_DEFAULT_SUPERIOR", Env: "SAFEDAY_DEFAULT_SUPERIOR"},
		{Key: "SAFEDAY_CALLBACK_TOKEN", Env: "SAFEDAY_CALLBACK_TOKEN", Sensitive: true},
		{Key: "SAFEDAY_SOFFICE_PATH", Env: "SAFEDAY_SOFFICE_PATH", Default: "soffice"},
		{Key: "PREVIEW_VIEWER_URL", Env: "PREVIEW_VIEWER_URL", Default: "https://view.officeapps.live.com/op/view.aspx"},
		{Key: "WORKLOG_DISPATCH_SYNC_TEAM", Env: "WORKLOG_DISPATCH_SYNC_TEAM"},
		{Key: "WORKLOG_DISPATCH_DEVICE_DDNS", Env: "WORKLOG_DISPATCH_DEVICE_DDNS"},
		{Key: "WORKLOG_DISPATCH_DEVICE_MAC", Env: "WORKLOG_DISPATCH_DEVICE_MAC"},
		{Key: "WORKLOG_DISPATCH_KVM_USER", Env: "WORKLOG_DISPATCH_KVM_USER"},
		{Key: "WORKLOG_DISPATCH_PREPARE_TIME", Env: "WORKLOG_DISPATCH_PREPARE_TIME", Default: "09:10"},
		{Key: "WORKLOG_DISPATCH_FETCH_TIME", Env: "WORKLOG_DISPATCH_FETCH_TIME", Default: "09:20"},
		{Key: "TREEHAZARD_SYNC_TEAM", Env: "TREEHAZARD_SYNC_TEAM", Default: "检修一班"},
		{Key: "TREEHAZARD_SYNC_DEVICE_DDNS", Env: "TREEHAZARD_SYNC_DEVICE_DDNS"},
		{Key: "TREEHAZARD_SYNC_DEVICE_MAC", Env: "TREEHAZARD_SYNC_DEVICE_MAC"},
		{Key: "TREEHAZARD_SYNC_KVM_USER", Env: "TREEHAZARD_SYNC_KVM_USER"},
		{Key: "TREEHAZARD_SYNC_CMD_WINDOW_MIN", Env: "TREEHAZARD_SYNC_CMD_WINDOW_MIN", Default: "5"},
		{Key: "TREEHAZARD_SYNC_PREPARE_TIME", Env: "TREEHAZARD_SYNC_PREPARE_TIME", Default: "23:50"},
		{Key: "TREEHAZARD_SYNC_FETCH_TIME", Env: "TREEHAZARD_SYNC_FETCH_TIME", Default: "00:00"},
		{Key: "CROSSING_SYNC_TEAM", Env: "CROSSING_SYNC_TEAM", Default: "检修一班"},
		{Key: "CROSSING_SYNC_DEVICE_DDNS", Env: "CROSSING_SYNC_DEVICE_DDNS"},
		{Key: "CROSSING_SYNC_DEVICE_MAC", Env: "CROSSING_SYNC_DEVICE_MAC"},
		{Key: "CROSSING_SYNC_KVM_USER", Env: "CROSSING_SYNC_KVM_USER"},
		{Key: "CROSSING_SYNC_CMD_WINDOW_MIN", Env: "CROSSING_SYNC_CMD_WINDOW_MIN", Default: "5"},
		{Key: "CROSSING_SYNC_PREPARE_TIME", Env: "CROSSING_SYNC_PREPARE_TIME", Default: "23:50"},
		{Key: "CROSSING_SYNC_FETCH_TIME", Env: "CROSSING_SYNC_FETCH_TIME", Default: "00:00"},
		{Key: "SGCC_JWT_SECRET", Env: "SGCC_JWT_SECRET", Sensitive: true},
		{Key: "SGCC_SM2_SERVER_PUB", Env: "SGCC_SM2_SERVER_PUB", Sensitive: true},
		{Key: "SGCC_SM2_CLIENT_PRIV", Env: "SGCC_SM2_CLIENT_PRIV", Sensitive: true},
		{Key: "SGCC_RSA_PUB", Env: "SGCC_RSA_PUB", Sensitive: true},
		{Key: "SGCC_RSA_PRIV", Env: "SGCC_RSA_PRIV", Sensitive: true},
		{Key: "SGCC_DCU_PUB", Env: "SGCC_DCU_PUB", Sensitive: true},
		{Key: "SGCC_WLA_PRIV", Env: "SGCC_WLA_PRIV", Sensitive: true},
		{Key: "SGCC_AD_SECRET", Env: "SGCC_AD_SECRET", Sensitive: true},
		{Key: "SGCC_DX_APPID", Env: "SGCC_DX_APPID"},
		{Key: "SGCC_VERSION", Env: "SGCC_VERSION", Default: "3.3.7"},
		{Key: "SGCC_VERSION_CODE", Env: "SGCC_VERSION_CODE", Default: "202609232038"},
		{Key: "SGCC_GRAY_VERSION", Env: "SGCC_GRAY_VERSION", Default: "2.4.7.1"},
		{Key: "SGCC_SYNC_TIME", Env: "SGCC_SYNC_TIME", Default: "23:00"},
		{Key: "SGCC_STARTCLOCK_REMIND_TIME", Env: "SGCC_STARTCLOCK_REMIND_TIME", Default: "11:00"},
		{Key: "SGCC_ENDCLOCK_REMIND_TIME", Env: "SGCC_ENDCLOCK_REMIND_TIME", Default: "18:00"},
		{Key: "SGCC_SYNC_CONCURRENCY", Env: "SGCC_SYNC_CONCURRENCY", Default: "4"},
		{Key: "SGCC_PHOTO_CONCURRENCY", Env: "SGCC_PHOTO_CONCURRENCY", Default: "2"},
		{Key: "SGCC_PROXY_URL", Env: "SGCC_PROXY_URL", Sensitive: true},
		{Key: "SGCC_PROXY_PROBE_INTERVAL_MS", Env: "SGCC_PROXY_PROBE_INTERVAL_MS", Default: "300000"},
		{Key: "JWT_EXPIRES", Env: "JWT_EXPIRES", Default: "7d"},
		{Key: "JWT_WEB_EXPIRES", Env: "JWT_WEB_EXPIRES", Default: "12h"},
		{Key: "WX_APPID", Env: "WX_APPID"},
		{Key: "WX_SECRET", Env: "WX_SECRET", Sensitive: true},
	}
}

// MergeSpecs 合并多组配置键规格（同一键后者覆盖前者），供装配层把模块自带规格
// （如 sgccclockin.ConfigSpecs）并入首次 SeedFromEnv 的导入注册表。
func MergeSpecs(groups ...[]Spec) []Spec {
	seen := map[string]int{}
	out := make([]Spec, 0)
	for _, group := range groups {
		for _, spec := range group {
			if idx, ok := seen[spec.Key]; ok {
				out[idx] = spec
				continue
			}
			seen[spec.Key] = len(out)
			out = append(out, spec)
		}
	}
	return out
}

// GroupForKey 给配置中心的展示分组；分组只影响界面，不改变配置键或生效逻辑。
func GroupForKey(key string) string {
	switch {
	case strings.HasPrefix(key, "WECHATPADPRO_"), key == "LOG_RAW_MESSAGE", key == "CHATLOG_DIGEST_TIME":
		return "微信智能应答"
	case strings.HasPrefix(key, "WORKLOG_"):
		return "出工日志"
	case strings.HasPrefix(key, "TREEHAZARD_"):
		return "树障隐患记录"
	case strings.HasPrefix(key, "CROSSING_"):
		return "交跨记录"
	case strings.HasPrefix(key, "SGCC_"):
		return "商旅打卡"
	case strings.HasPrefix(key, "NETDISK_"), strings.HasPrefix(key, "OO_"):
		return "团队网盘"
	case strings.HasPrefix(key, "SAFEDAY_"):
		return "安全日活动"
	case strings.HasPrefix(key, "GLKVM_"):
		return "远程控制"
	case strings.HasPrefix(key, "WEKNORA_"), key == "CALLME_INGEST_TOKEN":
		return "Call Me"
	case strings.HasPrefix(key, "APP_LOG_"):
		return "应用日志"
	default:
		return "通用集成"
	}
}
