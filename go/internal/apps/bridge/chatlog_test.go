package bridge

// ChatlogBridge 测试：chatlog 直调（token 校验 + parseBody/upsertFile 复用）。

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"shade/internal/apps/callme"
	"shade/internal/apps/chatlog"
	"shade/internal/apps/wechatanswer"
)

// fakeKB callme.KnowledgeAPI 桩（记录上传/删除）。
type fakeKB struct {
	uploadedName string
	uploadedPath string
	uploadedCh   string
	deleted      []string
	listItems    []callme.KnowledgeItem
	uploadID     string
	uploadErr    error
}

func (f *fakeKB) ListFolderKnowledge(context.Context, string, string) ([]callme.KnowledgeItem, error) {
	return f.listItems, nil
}

func (f *fakeKB) UploadKnowledgeFile(_ context.Context, _, fileName, title, channel string, _ []byte) (string, error) {
	f.uploadedName = title
	f.uploadedPath = fileName
	f.uploadedCh = channel
	if f.uploadErr != nil {
		return "", f.uploadErr
	}
	id := f.uploadID
	if id == "" {
		id = "kb-1"
	}
	return id, nil
}

func (f *fakeKB) DeleteKnowledge(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

var _ callme.KnowledgeAPI = (*fakeKB)(nil)

// chatlogCfg chatlog 直调配置（密钥 + WeKnora）。
func chatlogCfg(token string) fakeCfg {
	return fakeCfg{
		"CHATLOG_INGEST_TOKEN":  token,
		"WEKNORA_API_KEY":       "wk-key",
		"WEKNORA_CHATLOG_KB_ID": "kb-42",
	}
}

func TestChatlogBridgeDigestIngest(t *testing.T) {
	kb := &fakeKB{uploadID: "k-7"}
	cl := chatlog.New(chatlogCfg("tok-c"), kb)
	b := NewChatlogBridge(cl, chatlogCfg("tok-c"))
	err := b.Ingest(context.Background(), wechatanswer.ChatlogItem{
		Date: "2026-10-27", GroupName: "检修一班工作群", Kind: "digest", DigestMD: "# 纪要\n内容",
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if kb.uploadedPath != "2026/10.27/检修一班工作群_2026-10-27.md" {
		t.Fatalf("uploadedPath = %q", kb.uploadedPath)
	}
	if kb.uploadedCh != "chatlog" {
		t.Fatalf("channel = %q", kb.uploadedCh)
	}
}

func TestChatlogBridgeFileIngest(t *testing.T) {
	kb := &fakeKB{}
	cl := chatlog.New(chatlogCfg("tok-c"), kb)
	b := NewChatlogBridge(cl, chatlogCfg("tok-c"))
	body := base64.StdEncoding.EncodeToString([]byte("文档内容"))
	err := b.Ingest(context.Background(), wechatanswer.ChatlogItem{
		Date: "2026-10-27", GroupName: "g", Kind: "file",
		File: &wechatanswer.AskFile{Name: "记录.docx", DataBase64: body},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if kb.uploadedPath != "2026/10.27/记录.docx" {
		t.Fatalf("uploadedPath = %q", kb.uploadedPath)
	}
}

func TestChatlogBridgeTokenFailClosed(t *testing.T) {
	kb := &fakeKB{}
	cl := chatlog.New(chatlogCfg("tok-c"), kb)
	b := NewChatlogBridge(cl, chatlogCfg("tok-wrong"))
	err := b.Ingest(context.Background(), wechatanswer.ChatlogItem{
		Date: "2026-10-27", GroupName: "g", Kind: "digest", DigestMD: "x",
	})
	var ie *chatlog.IngestError
	if !errors.As(err, &ie) || ie.Status != 401 || ie.Code != "ingest_token_invalid" {
		t.Fatalf("err = %v", err)
	}

	// 密钥未配置 → 503 fail-closed。
	cl2 := chatlog.New(fakeCfg{"WEKNORA_API_KEY": "k", "WEKNORA_CHATLOG_KB_ID": "b"}, kb)
	b2 := NewChatlogBridge(cl2, fakeCfg{})
	err = b2.Ingest(context.Background(), wechatanswer.ChatlogItem{
		Date: "2026-10-27", GroupName: "g", Kind: "digest", DigestMD: "x",
	})
	if !errors.As(err, &ie) || ie.Status != 503 || ie.Code != "ingest_token_missing" {
		t.Fatalf("err = %v", err)
	}
}

func TestChatlogBridgeRejectsBadFile(t *testing.T) {
	kb := &fakeKB{}
	cl := chatlog.New(chatlogCfg("tok-c"), kb)
	b := NewChatlogBridge(cl, chatlogCfg("tok-c"))
	err := b.Ingest(context.Background(), wechatanswer.ChatlogItem{
		Date: "2026-10-27", GroupName: "g", Kind: "file",
		File: &wechatanswer.AskFile{Name: "run.exe", DataBase64: "aGVsbG8="},
	})
	var ie *chatlog.IngestError
	if !errors.As(err, &ie) || ie.Code != "unsupported_file" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(ie.Message, "不支持") {
		t.Fatalf("message = %q", ie.Message)
	}
}
