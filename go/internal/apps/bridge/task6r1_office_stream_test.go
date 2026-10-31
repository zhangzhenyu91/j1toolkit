package bridge

// Task 6 fix round 1：经 bridge.Wire + 真实 office.NewNetdiskDoc 的生产装配，
// 网盘文档源必须支持流式写回并落到 netdisk.OpenList.FsPut（不能只有测试桩可用）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"shade/internal/apps/netdisk"
	"shade/internal/apps/office"
)

// streamOL 记录 FsPut 的 OpenList 桩（netdisk.OpenListClient 接口）。
type streamOL struct {
	mu   sync.Mutex
	body []byte
	size int64
	puts int
}

func (s *streamOL) EnsureConfigured() error { return nil }

func (s *streamOL) Call(context.Context, string, string, netdisk.CallOpts) (json.RawMessage, error) {
	return json.RawMessage("null"), nil
}

func (s *streamOL) DownloadStream(context.Context, string) (io.ReadCloser, http.Header, error) {
	return io.NopCloser(strings.NewReader("")), http.Header{}, nil
}

func (s *streamOL) EnsureDir(context.Context, string) error { return nil }
func (s *streamOL) PutEmpty(context.Context, string) error  { return nil }

func (s *streamOL) FsPut(_ context.Context, _ string, body io.Reader, size int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	s.size = size
	s.body, _ = io.ReadAll(body)
	return nil
}

func (s *streamOL) ArchiveDownloadStream(context.Context, string, string, string) (io.ReadCloser, http.Header, error) {
	return nil, nil, nil
}

// TestWireOfficeNetdiskStreams 生产装配后 office.Module.Netdisk 必须实现流式写回，
// 且经 netdisk.Module.WriteFileStream 真正写入 FsPut。
func TestWireOfficeNetdiskStreams(t *testing.T) {
	ol := &streamOL{}
	nd := netdisk.New(nil)
	nd.OL = ol
	nd.Base = netdisk.BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	om := office.New(nil)

	Wire(Deps{Office: om, Netdisk: nd})

	sw, ok := om.Netdisk.(office.NetdiskStreamWriter)
	if !ok {
		t.Fatal("bridge.Wire 装配后的 office.Netdisk 必须实现 NetdiskStreamWriter")
	}
	payload := "streamed-doc"
	if err := sw.WriteFileStream(context.Background(), "my", "alice", 0, "/docs/a.docx",
		strings.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("WriteFileStream: %v", err)
	}
	ol.mu.Lock()
	defer ol.mu.Unlock()
	if ol.puts != 1 || string(ol.body) != payload || ol.size != int64(len(payload)) {
		t.Fatalf("FsPut 未收到流式内容: puts=%d body=%q size=%d", ol.puts, ol.body, ol.size)
	}
}
