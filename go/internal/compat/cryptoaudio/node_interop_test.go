package cryptoaudio

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 与现有 Node sm-crypto 的互操作测试（可选）：node 与 server/node_modules/sm-crypto
// 可用时执行双向互测；缺任一即跳过。全部使用随机生成的测试密钥，不触任何外部服务。
//
// 口径（server/src/sgccclockin/protocol.js）：
//   - sm2.doEncrypt(text, pub, 0) → C1C2C3 hex（C1 无 04 前缀），协议层 wire 格式再补 '04'；
//   - sm2.doDecrypt(wire.slice(2), priv, 0) 解出明文。

// nodeSm2Env 定位 node 与 sm-crypto 模块目录；不可用返回 false。
func nodeSm2Env(t *testing.T) (nodePath, workDir string) {
	t.Helper()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未安装 node，跳过 sm-crypto 互操作测试")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Skipf("取不到工作目录: %v", err)
	}
	for i := 0; i < 8; i++ {
		cand := filepath.Join(dir, "server", "node_modules", "sm-crypto")
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			return nodePath, filepath.Join(dir, "server")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skip("未找到 server/node_modules/sm-crypto，跳过 sm-crypto 互操作测试")
	return "", ""
}

// runNode 执行 node -e 脚本（cwd=server，可 require sm-crypto），返回去空白 stdout。
func runNode(t *testing.T, nodePath, workDir, script string, args ...string) string {
	t.Helper()
	cmd := exec.Command(nodePath, append([]string{"-e", script}, args...)...)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node 执行失败: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestNodeSmCryptoInterop(t *testing.T) {
	nodePath, workDir := nodeSm2Env(t)

	// 1) node 生成密钥对（sm-crypto 口径 hex）
	out := runNode(t, nodePath, workDir,
		`const {sm2}=require('sm-crypto');const k=sm2.generateKeyPairHex();console.log(k.privateKey+' '+k.publicKey);`)
	parts := strings.Fields(out)
	if len(parts) != 2 {
		t.Fatalf("密钥对输出异常: %q", out)
	}
	privHex, pubHex := parts[0], parts[1]
	if _, err := ParseSM2PrivateKey(privHex); err != nil {
		t.Fatalf("sm-crypto 私钥无法解析: %v", err)
	}
	if _, err := ParseSM2PublicKey(pubHex); err != nil {
		t.Fatalf("sm-crypto 公钥无法解析: %v", err)
	}

	// 2) Go 加密 → node 解密（wire 格式 slice(2) 口径）
	const goMsg = "Go侧互通测试ABC"
	ctHex, err := SM2EncryptHex(goMsg, pubHex)
	if err != nil {
		t.Fatal(err)
	}
	out = runNode(t, nodePath, workDir,
		`const {sm2}=require('sm-crypto');const [priv,ct]=process.argv.slice(1);`+
			`console.log(sm2.doDecrypt(ct.slice(2),priv,0));`,
		privHex, ctHex)
	if out != goMsg {
		t.Fatalf("node 解密 = %q，期望 %q", out, goMsg)
	}

	// 3) node 加密 → Go 解密
	const nodeMsg = "Node侧互通消息"
	out = runNode(t, nodePath, workDir,
		`const {sm2}=require('sm-crypto');const [priv,pub]=process.argv.slice(1);`+
			`console.log(priv+' '+sm2.doEncrypt(process.argv[3],pub,0));`,
		privHex, pubHex, nodeMsg)
	f := strings.Fields(out)
	if len(f) != 2 {
		t.Fatalf("node 加密输出异常: %q", out)
	}
	got, err := SM2DecryptHex("04"+f[1], f[0])
	if err != nil {
		t.Fatal(err)
	}
	if got != nodeMsg {
		t.Fatalf("Go 解密 = %q，期望 %q", got, nodeMsg)
	}
}
