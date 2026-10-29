package apps

import (
	"testing"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// 环境自检入口测试：mock 模式离线走通 SSO 自检；未知类型明确报错。

func TestRunSelftestUnknownKind(t *testing.T) {
	j := &job.Job{}
	if _, err := RunSelftest("no_such_kind", j, nil); err == nil {
		t.Fatal("未知自检类型应报错")
	}
}

func TestRunSelftestSSOMock(t *testing.T) {
	t.Setenv("UVMP_TOOLKIT_CONFIG", t.TempDir()+"/config.json")
	t.Setenv("UVMP_TOOLKIT_CONFIG_INI", t.TempDir()+"/none.ini")
	state.SetRoot(t.TempDir())
	t.Cleanup(func() { state.SetRoot("") })
	if _, err := config.Save(config.Dict{"dev": config.Dict{"mock": true}}); err != nil {
		t.Fatal(err)
	}
	j := &job.Job{}
	res, err := RunSelftest("sso", j, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res["ok"] != true || res["mock"] != true {
		t.Fatalf("res = %+v", res)
	}
	if n, _ := res["token_len"].(int); n != 9 {
		t.Fatalf("mock token_len = %+v", res["token_len"])
	}
}
