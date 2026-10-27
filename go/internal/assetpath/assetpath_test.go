package assetpath

import (
	"os"
	"path/filepath"
	"testing"
)

// mkdirp 建目录（测试素材）。
func mkdirp(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
}

// touch 建空文件（测试素材）。
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("建文件失败: %v", err)
	}
}

// TestResolveContainerLayout 容器布局（WORKDIR 下 assets/… 命中）返回相对路径。
func TestResolveContainerLayout(t *testing.T) {
	root := t.TempDir()
	mkdirp(t, filepath.Join(root, "assets", "fonts"))
	t.Chdir(root)

	if got := Resolve("fonts"); got != filepath.Join("assets", "fonts") {
		t.Fatalf("容器布局应返回相对路径 assets/fonts，got %q", got)
	}
}

// TestResolveRepoLayout 仓根开发布局（server/assets/… 命中）返回相对路径。
func TestResolveRepoLayout(t *testing.T) {
	root := t.TempDir()
	mkdirp(t, filepath.Join(root, "server", "assets", "worklog"))
	t.Chdir(root)

	if got := Resolve("worklog"); got != filepath.Join("server", "assets", "worklog") {
		t.Fatalf("仓根布局应返回相对路径 server/assets/worklog，got %q", got)
	}
}

// TestResolveWalkUp 自子目录向上命中仓根布局，返回可直接使用的拼接路径。
func TestResolveWalkUp(t *testing.T) {
	root := t.TempDir()
	mkdirp(t, filepath.Join(root, "server", "assets", "fonts"))
	sub := filepath.Join(root, "go", "internal", "apps", "wmadd")
	mkdirp(t, sub)
	t.Chdir(sub)

	got := Resolve("fonts")
	want := filepath.Join(root, "server", "assets", "fonts")
	if got != want {
		t.Fatalf("向上命中应返回拼接路径 %q，got %q", want, got)
	}
}

// TestResolveFile 文件探测：容器布局命中相对路径；目录不算命中。
func TestResolveFile(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "assets", "safeday", "activity-record-template.docx"))
	mkdirp(t, filepath.Join(root, "assets", "worklog"))
	t.Chdir(root)

	if got := ResolveFile("safeday/activity-record-template.docx"); got != filepath.Join("assets", "safeday", "activity-record-template.docx") {
		t.Fatalf("文件探测应回 assets/safeday/…，got %q", got)
	}
	// 目录在文件探测下不算命中
	if got := ResolveFile("worklog"); got != "" {
		t.Fatalf("目录不应被文件探测命中，got %q", got)
	}
	// 文件在目录探测下不算命中
	if got := Resolve("safeday/activity-record-template.docx"); got != "" {
		t.Fatalf("文件不应被目录探测命中，got %q", got)
	}
}

// TestResolveMiss 两套布局均未命中返回空串。
func TestResolveMiss(t *testing.T) {
	t.Chdir(t.TempDir())
	if got := Resolve("fonts"); got != "" {
		t.Fatalf("未命中应返回空串，got %q", got)
	}
	if got := ResolveFile("safeday/none.docx"); got != "" {
		t.Fatalf("未命中应返回空串，got %q", got)
	}
}

// TestResolveContainerFirst 容器与仓根布局同时存在时容器布局优先。
func TestResolveContainerFirst(t *testing.T) {
	root := t.TempDir()
	mkdirp(t, filepath.Join(root, "assets", "worklog"))
	mkdirp(t, filepath.Join(root, "server", "assets", "worklog"))
	t.Chdir(root)

	if got := Resolve("worklog"); got != filepath.Join("assets", "worklog") {
		t.Fatalf("容器布局应优先，got %q", got)
	}
}
