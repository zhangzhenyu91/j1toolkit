// WeChatPadPro 媒体收发扩展：群文件发送（WF3 回答 md）与文件/视频/语音本体下载。
// 下载端点与参数形态移植 crane-answer src/wechatpadpro/fileStore.js（网关参数小写驼峰，
// 分片 512KB，单件上限 50MB）；语音下载后经 internal/compat/cryptoaudio 转码
// （wav/mp3 直出，silk 解码封装 WAV），口径同 fileStore.js convertVoice。
package wechatanswer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"shade/internal/compat/cryptoaudio"
)

const (
	mediaChunkSize   = 512 * 1024
	mediaMaxBytes    = 50 << 20
	mediaDlRetries   = 2
	mediaDlRetryWait = 3 * time.Second
)

// SendFile 发送群文件（回答 md 等）。
func (c *PadProClient) SendFile(ctx context.Context, groupID string, data []byte, fileName string) error {
	_, err := c.post(ctx, "/v1/Msg/SendFile", map[string]any{
		"Base64":   base64.StdEncoding.EncodeToString(data),
		"FileName": fileName,
		"ToWxid":   groupID,
	})
	return err
}

var (
	_ FileSender     = (*PadProClient)(nil)
	_ FileDownloader = (*PadProClient)(nil)
)

// DownloadMedia 按缓存参数下载媒体本体（ext 为建议扩展名）。
func (c *PadProClient) DownloadMedia(ctx context.Context, e MediaEntry) ([]byte, string, error) {
	switch e.Kind {
	case "file":
		if e.AttachID == "" {
			return nil, "", errors.New("缺少 attachid，无法下载文件")
		}
		if e.DataLen <= 0 {
			return nil, "", errors.New("缺少 totallen，无法分片下载文件")
		}
		buf, err := c.downloadChunked(ctx, "/v1/Tools/DownloadFileBinary", e.DataLen, func(start, length int64) map[string]any {
			appID := e.AppID
			if appID == "" {
				appID = c.loadCfg(ctx).FileAppID
			}
			return map[string]any{
				"app_id":    appID,
				"attach_id": e.AttachID,
				"data_len":  e.DataLen,
				"file_name": e.FileName,
				"user_name": firstNonEmpty(e.FromUserName, e.SenderID),
				"section":   map[string]any{"start_pos": start, "data_len": length},
			}
		})
		if err != nil {
			return nil, "", err
		}
		return buf, fileExtLower(e.FileName), nil
	case "video":
		if e.MsgID == "" {
			return nil, "", errors.New("缺少 msg_id，无法下载视频")
		}
		if e.DataLen <= 0 {
			return nil, "", errors.New("缺少 data_len，无法分片下载视频")
		}
		buf, err := c.downloadChunked(ctx, "/v1/Tools/DownloadVideo", e.DataLen, func(start, length int64) map[string]any {
			return map[string]any{
				"compress_type": 0,
				"data_len":      e.DataLen,
				"msg_id":        e.MsgID,
				"section":       map[string]any{"start_pos": start, "data_len": length},
				"to_wxid":       c.loadCfg(ctx).BotWxid,
			}
		})
		if err != nil {
			return nil, "", err
		}
		return buf, "mp4", nil
	case "voice":
		if (e.MsgID == "" && e.NewMsgID == "") || e.MasterBufID == "" || e.Length <= 0 {
			return nil, "", errors.New("语音参数不全，无法下载")
		}
		if e.Length > mediaMaxBytes {
			return nil, "", &AskError{Code: "file_too_large", Message: "语音超过 50MB 上限"}
		}
		body, err := c.post(ctx, "/v1/Tools/DownloadVoiceBinary", map[string]any{
			"chat_room_name": e.GroupID,
			"client_msg_id":  firstNonEmpty(e.MsgID, e.NewMsgID),
			"file_name":      "voice.silk",
			"format":         1,
			"from_user_name": firstNonEmpty(e.FromUserName, e.SenderID),
			"length":         e.Length,
			"master_buf_id":  e.MasterBufID,
			"msg_id":         e.MsgID,
			"new_msg_id":     firstNonEmpty(e.NewMsgID, e.SvrID),
			"to_user_name":   c.loadCfg(ctx).BotWxid,
		})
		if err != nil {
			return nil, "", err
		}
		buf, err := extractMediaBinary(body)
		if err != nil {
			return nil, "", err
		}
		// 格式探测与转码（wav/mp3 直出，silk 解码封装 WAV），口径同 fileStore.js convertVoice
		conv, err := cryptoaudio.ConvertVoice(buf)
		if err != nil {
			return nil, "", fmt.Errorf("语音转码失败：%w", err)
		}
		return conv.Data, conv.Ext, nil
	default:
		return nil, "", fmt.Errorf("未知媒体类型：%s", e.Kind)
	}
}

// downloadChunked 分片下载（单片 512KB，总长以 dataLen 为准；失败 3s 间隔重试 2 次）。
func (c *PadProClient) downloadChunked(ctx context.Context, path string, dataLen int64, buildBody func(start, length int64) map[string]any) ([]byte, error) {
	if dataLen > mediaMaxBytes {
		return nil, &AskError{Code: "file_too_large", Message: "文件超过 50MB 上限"}
	}
	var out []byte
	for pos := int64(0); pos < dataLen; pos += mediaChunkSize {
		length := int64(mediaChunkSize)
		if pos+length > dataLen {
			length = dataLen - pos
		}
		body, err := c.postWithRetry(ctx, path, buildBody(pos, length))
		if err != nil {
			return nil, err
		}
		chunk, err := extractMediaBinary(body)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
	}
	return out, nil
}

// postWithRetry 网关调用失败重试（共 3 次尝试，间隔 3s）。
func (c *PadProClient) postWithRetry(ctx context.Context, path string, body any) (map[string]any, error) {
	var lastErr error
	for attempt := 0; attempt <= mediaDlRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(mediaDlRetryWait):
			}
		}
		parsed, err := c.post(ctx, path, body)
		if err == nil {
			return parsed, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// extractMediaBinary 返回二进制可能 base64 包裹：探测 Data 下 Buffer/Data/Image/File 等字段。
func extractMediaBinary(parsed map[string]any) ([]byte, error) {
	tryDecode := func(s string) []byte {
		if s == "" {
			return nil
		}
		if buf, err := base64.StdEncoding.DecodeString(s); err == nil && len(buf) > 0 {
			return buf
		}
		return nil
	}
	if data, ok := parsed["Data"].(string); ok {
		if buf := tryDecode(data); buf != nil {
			return buf, nil
		}
	}
	if data, ok := parsed["Data"].(map[string]any); ok {
		for _, key := range []string{"Buffer", "Data", "Image", "File", "Content", "Binary", "buffer", "data", "image", "file"} {
			if s, ok := data[key].(string); ok {
				if buf := tryDecode(s); buf != nil {
					return buf, nil
				}
			}
		}
	}
	return nil, errors.New("媒体下载接口未返回二进制数据")
}

// fileExtLower 小写扩展名（不含点；无扩展名返回空）。
func fileExtLower(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			ext := name[i+1:]
			if len(ext) > 8 {
				return ""
			}
			out := make([]byte, 0, len(ext))
			for j := 0; j < len(ext); j++ {
				ch := ext[j]
				if ch >= 'A' && ch <= 'Z' {
					ch += 'a' - 'A'
				}
				out = append(out, ch)
			}
			return string(out)
		}
		if name[i] == '/' || name[i] == '\\' {
			return ""
		}
	}
	return ""
}
