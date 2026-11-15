# -*- coding: utf-8 -*-
"""构建/安装/发布配置的静态门禁（不构建、不联网、不读真实密钥）。

盯住一批「改了一处忘另一处」的配置契约：
- 内置 CPython 版本与 SHA256 单一来源（packaging/python-runtime.env），构建脚本里不许出现
  latest 回退；
- Python 依赖全钉版本（packaging/requirements-core.txt），且 gmssl 的 Cryptodome 依赖在内；
- 安装包契约：deb 包名 uvmp-toolkit-desktop、afterInstall 按同一包名查版本、NSIS 写 version.txt；
- 服务端/应用构建必须收编两个 trial 引擎源码，打包排除联调调试脚本；
- PyInstaller onedir 布局：6 把 --add-data 资产放进 _internal/（_MEIPASS），5 放 exe 同目录，
  两处精确位置都要认，且不许用宽泛 find 把整包缺件掩盖成通过；
- 发布链路生成并校验 SHA256SUMS，且不再回退公共代理。
"""

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent  # uvmp-toolkit/
PACKAGING = ROOT / "packaging"
DESKTOP = ROOT / "desktop"
REPO = ROOT.parent


def _read(path):
    return Path(path).read_text(encoding="utf-8-sig")


class PythonRuntimeTest(unittest.TestCase):
    def setUp(self):
        self.env = _read(PACKAGING / "python-runtime.env")

    def test_版本与发布号固定(self):
        self.assertIn("PYVER=3.15.2", self.env)
        self.assertIn("PYREL=20261110", self.env)

    def test_四平台校验和均为64位十六进制(self):
        shas = re.findall(r"^PY_SHA_\w+=([0-9a-f]+)$", self.env, re.M)
        self.assertEqual(len(shas), 4, shas)
        for sha in shas:
            self.assertEqual(len(sha), 64, sha)

    def test_资产品名与版本一致(self):
        self.assertIn(
            'cpython-${PY_TAG}-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz', self.env
        )
        self.assertIn(
            'cpython-${PY_TAG}-aarch64-unknown-linux-gnu-install_only_stripped.tar.gz', self.env
        )

    def test_linux容器脚本引用单一来源且无latest回退(self):
        text = _read(PACKAGING / "build_linux_container.sh")
        self.assertIn("packaging/python-runtime.env", text)
        self.assertIn("sha256sum -c -", text)
        self.assertNotIn("releases/latest", text)
        self.assertNotIn('download("latest")', text)
        self.assertNotRegex(text, r"PYVER=3\.\d+\.\d+\s*$")  # 不许再在脚本里另写版本


class RequirementsPinTest(unittest.TestCase):
    def setUp(self):
        self.lines = [
            ln.strip()
            for ln in _read(PACKAGING / "requirements-core.txt").splitlines()
            if ln.strip() and not ln.strip().startswith("#")
        ]

    def test_全部条目钉死版本(self):
        bare = [ln for ln in self.lines if "==" not in ln]
        self.assertEqual(bare, [], "存在未钉版本的依赖：%s" % bare)
        for ln in self.lines:
            self.assertNotRegex(ln, r"(>=|~=|!=)", ln)

    def test_关键运行期依赖在内(self):
        joined = "\n".join(self.lines)
        for pkg in (
            "pyinstaller==",
            "requests==",
            "pypdf==",
            "websocket-client==",
            "gmssl==",
            "pycryptodomex==",  # gmssl.sm2 的必需依赖，漏了自动登录必挂
            "playwright==",
            "greenlet==",
        ):
            self.assertIn(pkg, joined)

    def test_构建脚本统一引用且做pip_check(self):
        for name in ("build_unix.sh", "build_on_device.sh", "build_local.ps1"):
            text = _read(PACKAGING / name)
            self.assertIn("requirements-core.txt", text, name)
            self.assertIn("pip check", text, name)

    def test_不再裸装依赖(self):
        for name in ("build_unix.sh", "build_on_device.sh", "build_local.ps1"):
            text = _read(PACKAGING / name)
            self.assertNotRegex(text, r"pip install[^\n]*playwright==?1\.62", name)


class TrialEngineBundleTest(unittest.TestCase):
    """课程自动学习 / 机巡审核引擎是「按路径运行时加载」的源码资产，漏收只有真跑才炸。"""

    SCRIPTS = ("build_unix.sh", "build_on_device.sh", "build_local.ps1")

    def test_构建脚本收编两个trial引擎(self):
        for name in self.SCRIPTS:
            text = _read(PACKAGING / name)
            self.assertIn("course_autoplay_trial.py", text, name)
            self.assertIn("defect_review_trial.py", text, name)

    def test_构建脚本断言trial引擎存在(self):
        for name in self.SCRIPTS:
            text = _read(PACKAGING / name)
            # 装配完整性断言里含 run-usb-agent.vbs 的那一段，必须同时列出两个 trial 文件
            m = re.search(r"(?:foreach \(\$f in @\(|\bfor f in )(.*?run-usb-agent\.vbs.*?)(?:\)\s*\{|\bdo\b)", text, re.S)
            self.assertIsNotNone(m, name)
            block = m.group(1)
            self.assertIn("course_autoplay_trial.py", block, name)
            self.assertIn("defect_review_trial.py", block, name)

    def test_CI工作流收编并断言trial引擎(self):
        text = _read(REPO / ".github" / "workflows" / "release.yml")
        self.assertIn("--add-data \"course_autoplay_trial.py;.\"", text)
        self.assertIn("--add-data \"defect_review_trial.py;.\"", text)
        self.assertIn("course_autoplay_trial.py\", \"defect_review_trial.py\"", text)


class CoreBundleLayoutTest(unittest.TestCase):
    """装配门禁必须同时认 PyInstaller 6 的 _internal/ 布局与 5 的 exe 同目录布局。

    v2.3.43 的 linux amd64/arm64 都在这里挂掉：--add-data 的资产其实落在
    dist/uvmp-core/_internal/course_autoplay_trial.py（PyInstaller 6 onedir，sys._MEIPASS 即该目录），
    断言却只查 $root/$f → 双双报「核心缺少 course_autoplay_trial.py」。
    兼容两种布局不能退化成递归 find：那样「整包缺件」会被别处的同名文件掩盖。
    """

    BASH_SCRIPTS = ("build_unix.sh", "build_on_device.sh")
    PAYLOAD = (
        "config.ini",
        "config.example.json",
        "config.initial.json",
        "run-usb-agent.vbs",
        "course_autoplay_trial.py",
        "defect_review_trial.py",
        "apps/pcd_export/parallel.py",
        "apps/pcd_export/backend.py",
        "install/linux/uvmp-toolkit.service",
    )

    def test_bash定位函数只认根目录与_internal两处(self):
        for name in self.BASH_SCRIPTS:
            text = _read(PACKAGING / name)
            body = _shell_function(text, "core_payload_path", name)
            self.assertIn('"$root/_internal"', body, name)
            self.assertIn("local root=", body, name)
            # 只做两个精确路径的 -f 判断；出现 find/-name/-path 就等于放宽成递归搜索
            self.assertNotRegex(body, r"\bfind\b|-name\b|-path\b|-maxdepth\b", name)
            self.assertEqual(body.count("for base in"), 1, name)
            self.assertIn('[ -f "$base/$rel" ]', body, name)

    def test_bash断言逐项走定位函数而非只看根目录(self):
        for name in self.BASH_SCRIPTS:
            text = _read(PACKAGING / name)
            self.assertIn('core_payload_path "$root" "$rel"', text, name)
            self.assertIn('core_payload_path "$root" "$f"', text, name)
            # 旧写法：根目录直读 / 递归 endsWith 匹配，均不许回潮
            self.assertNotIn('[ -f "$root/$f" ]', text, name)
            self.assertNotRegex(text, r'find "\$root" -path "\*/\$rel"', name)

    def test_bash定位函数对两种真实布局命中且整包缺件仍失败(self):
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        for name in self.BASH_SCRIPTS:
            fn = _shell_function(_read(PACKAGING / name), "core_payload_path", name)
            with tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp) / "uvmp-core"
                root.mkdir()
                (root / "uvmp-core").write_text("", encoding="utf-8")
                # 布局 A（PyInstaller 5）：资产与 exe 同目录
                for rel in self.PAYLOAD:
                    _touch(root / rel)
                for rel in self.PAYLOAD:
                    # Git Bash 打印正斜杠、Windows 的 str(Path) 是反斜杠：按规范化路径比较
                    self.assertEqual(
                        _norm_path(_run_shell_payload(bash, tmp, fn, root, rel)),
                        _norm_path(root / rel),
                        "%s 未认 exe 同目录布局：%s" % (name, rel),
                    )
                # 布局 B（PyInstaller 6）：资产在 _internal/，根目录没有
                for rel in self.PAYLOAD:
                    (root / rel).unlink()
                    _touch(root / "_internal" / rel)
                for rel in self.PAYLOAD:
                    self.assertEqual(
                        _norm_path(_run_shell_payload(bash, tmp, fn, root, rel)),
                        _norm_path(root / "_internal" / rel),
                        "%s 未认 _internal 布局：%s" % (name, rel),
                    )
                # 整包缺件：同名文件只存在于无关深层目录，断言必须失败（不许被宽泛搜索救活）
                for rel in self.PAYLOAD:
                    (root / "_internal" / rel).unlink()
                for rel in ("course_autoplay_trial.py", "defect_review_trial.py", "config.ini"):
                    _touch(root / "unrelated" / "deep" / rel)
                for rel in ("course_autoplay_trial.py", "defect_review_trial.py", "config.ini"):
                    self.assertIsNone(
                        _run_shell_payload(bash, tmp, fn, root, rel),
                        "%s 把深层同名文件当成了装配产物：%s" % (name, rel),
                    )
                # 同名目录不算文件：--add-data 漏收时若只 -e 判断，空目录会被误判为通过
                for rel in ("course_autoplay_trial.py", "config.ini"):
                    (root / rel).mkdir()
                for rel in ("course_autoplay_trial.py", "config.ini"):
                    self.assertIsNone(
                        _run_shell_payload(bash, tmp, fn, root, rel),
                        "%s 把同名目录当成了装配产物：%s" % (name, rel),
                    )

    def test_powershell脚本同样双位置且无递归匹配(self):
        text = _read(PACKAGING / "build_local.ps1")
        body = _powershell_function(text, "Get-CorePayloadPath")
        self.assertIn('Join-Path $root "_internal"', body)
        self.assertNotRegex(body, r"-Recurse")
        # 精确路径 + 必须是文件：同名目录不能让「漏收」蒙混过关
        self.assertIn("-LiteralPath", body)
        self.assertIn("-PathType Leaf", body)
        self.assertIn("Get-CorePayloadPath $root $rel", text)
        self.assertIn("Get-CorePayloadPath $root $f", text)
        # 旧写法：递归 Get-ChildItem + EndsWith，会把深层同名文件当命中
        self.assertNotIn("$_.FullName.Replace", text)
        self.assertNotIn('Test-Path (Join-Path $root $f)', text)
        # PowerShell 5.1 按 BOM 判定编码，去掉 BOM 会让中文注释按 ANSI 解成乱码
        self.assertTrue(
            (PACKAGING / "build_local.ps1").read_bytes().startswith(b"\xef\xbb\xbf"),
            "build_local.ps1 必须保留 UTF-8 BOM",
        )

    def test_CI工作流内联断言同样双位置(self):
        text = _job_block(_read(REPO / ".github" / "workflows" / "release.yml"), "client-windows")
        self.assertIn('"dist\\uvmp-core\\_internal"', text)
        for rel in ("course_autoplay_trial.py", "defect_review_trial.py", "config.initial.json"):
            self.assertIn(rel, text)
        self.assertIn("-LiteralPath", text)
        self.assertIn("-PathType Leaf", text)
        self.assertNotIn('Join-Path "dist\\uvmp-core" $f', text)

    def test_powershell定位函数行为一致(self):
        pwsh = shutil.which("pwsh")
        if not pwsh:
            self.skipTest("无 pwsh，无法重放 PowerShell 断言")
        fn = _powershell_function(_read(PACKAGING / "build_local.ps1"), "Get-CorePayloadPath")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "uvmp-core"
            (root / "_internal").mkdir(parents=True)
            _touch(root / "_internal" / "course_autoplay_trial.py")
            _touch(root / "unrelated" / "deep" / "defect_review_trial.py")
            self.assertEqual(
                _run_pwsh_payload(pwsh, tmp, fn, root, "course_autoplay_trial.py").strip(),
                str(root / "_internal" / "course_autoplay_trial.py"),
            )
            self.assertEqual(
                _run_pwsh_payload(pwsh, tmp, fn, root, "defect_review_trial.py").strip(), ""
            )


class ElectronBuilderConfigTest(unittest.TestCase):
    def setUp(self):
        self.text = _read(DESKTOP / "electron-builder.yml")

    def test_linux_desktop按electron_builder26的entry结构书写(self):
        """electron-builder 26 的 scheme 里 linux.desktop 只认 entry / desktopActions。

        直接把 Name/Comment/StartupWMClass 写在 desktop 下会被判 additionalProperties 违规，
        打包在读取配置阶段就中止（`--dir` 也一样）。这里按缩进解析该块，断言：
        1) desktop 的直接子键只有 schema 允许的那几个；
        2) [Desktop Entry] 的键都写在 entry 里。
        """
        desktop = _yaml_block(self.text, "desktop", parent="linux")
        self.assertTrue(desktop, "linux.desktop 块不存在或缩进异常")
        allowed = {"entry", "desktopActions"}
        self.assertTrue(set(desktop) <= allowed, "linux.desktop 下出现了 schema 不允许的键：%s" % desktop)
        entry = _yaml_block(self.text, "entry", parent="linux")
        for key in ("Name", "Comment", "StartupWMClass"):
            self.assertIn(key, entry, "desktop.entry 缺少 %s" % key)

    def test_配置能过electron_builder自带schema(self):
        """直接拿 electron-builder 自己的 scheme.json 校验整份配置。

        这是最贴近打包时那次校验的断言（同一份 schema、同一套 ajv），能把
        「某个键在 26 里换了层级」这类问题在 CI 门禁里挡住，而不是等到打包才炸。
        没有 node_modules / node 的环境（构建容器先跑门禁后 npm ci）跳过。
        """
        scheme_path = DESKTOP / "node_modules" / "app-builder-lib" / "scheme.json"
        if not scheme_path.is_file():
            self.skipTest("未安装 desktop/node_modules（先 npm ci 再跑本断言）")
        node = shutil.which("node")
        if node is None:
            self.skipTest("环境中没有 node")
        # 复用 electron-builder 依赖树里的 ajv + js-yaml，不引入新的 Python 依赖
        script = DESKTOP / ".uvmp-schema-check.cjs"
        script.write_text(_SCHEMA_CHECK_JS, encoding="utf-8")
        try:
            out = subprocess.run(
                [node, str(script), str(DESKTOP / "electron-builder.yml"), str(scheme_path)],
                capture_output=True,
                text=True,
                timeout=120,
            )
        finally:
            script.unlink(missing_ok=True)
        self.assertEqual(out.returncode, 0, "electron-builder.yml 违反 schema：\n%s\n%s" % (out.stdout, out.stderr))

    def test_deb包名统一(self):
        self.assertIn("packageName: uvmp-toolkit-desktop", self.text)
        after = _read(DESKTOP / "build" / "afterInstall.sh")
        self.assertIn('PKG_NAME="uvmp-toolkit-desktop"', after)
        # 旧包名查询会让 version.txt 永远写不出来（曾长期存在）
        self.assertNotRegex(after, r'dpkg -s uvmp-toolkit 2')

    def test_deb声明图形与音频运行库(self):
        for dep in (
            "libgtk-3-0",
            "libnss3",
            "libgbm1",
            "libasound2",
            "libatk1.0-0",
            "libcups2",
            "libxkbcommon0",
            "libxtst6",
        ):
            self.assertIn(dep, self.text, dep)

    def test_安装钩子补sandbox与桌面刷新(self):
        after = _read(DESKTOP / "build" / "afterInstall.sh")
        self.assertIn("chrome-sandbox", after)
        self.assertIn("chmod 4755", after)
        self.assertIn("update-desktop-database", after)
        self.assertIn("gtk-update-icon-cache", after)
        # 整体替换模板后必须自己补回 /usr/bin 软链
        self.assertIn("update-alternatives --install", after)

    def test_windows写入version_txt(self):
        nsh = _read(DESKTOP / "build" / "installer.nsh")
        self.assertIn("version.txt", nsh)
        self.assertIn("${VERSION}", nsh)

    def test_打包排除调试脚本(self):
        self.assertIn("'!electron/quizsearch/tools/**'", self.text)

    def test_渲染层Excel解析器放devDependencies(self):
        """两个解析器由 Vite 打进 dist，不能放 dependencies。

        electron-builder 只按 production dependencies 收 node_modules：放 dependencies 会被
        重复收进 asar（曾致 asar 205MB）。主进程不引解析器（只取文件字节），因此 devDependencies
        是唯一正确位置——同时必须留在 lock 里，否则 npm ci --omit=dev 之外的构建装不上。
        """
        import json

        pkg = json.loads(_read(DESKTOP / "package.json"))
        for name in ("read-excel-file", "write-excel-file"):
            self.assertIn(name, pkg["devDependencies"], name)
            self.assertNotIn(name, pkg["dependencies"], "%s 不应出现在 dependencies" % name)
        # 主进程只取字节，不得 require 解析器
        main = _read(DESKTOP / "electron" / "main.cjs")
        self.assertNotRegex(main, r"require\(['\"](read|write)-excel-file")
        # 渲染层模块确实引用它们（.cjs / .mjs 形态随复用方案演进，这里只认「文件存在且引用」）
        for stem in ("workbook", "template"):
            found = sorted((DESKTOP / "electron" / "quizsearch").glob(stem + ".*"))
            self.assertTrue(found, "找不到题库 Excel 模块 %s.*" % stem)
            self.assertTrue(
                any("excel-file" in _read(p) for p in found), found
            )

    def test_lockfile里两个解析器标记为dev(self):
        """package.json 与 package-lock.json 的归属必须一致，否则 npm ci 会报 lock 不同步。"""
        import json

        lock = json.loads(_read(DESKTOP / "package-lock.json"))
        root = lock["packages"][""]
        for name in ("read-excel-file", "write-excel-file"):
            self.assertIn(name, root["devDependencies"], name)
            self.assertNotIn(name, root["dependencies"], name)
            entry = lock["packages"]["node_modules/" + name]
            self.assertTrue(entry.get("dev"), "%s 在 lock 里应标记 dev" % name)
            # 版本不受归属调整影响
            self.assertRegex(entry["version"], r"^\d+\.\d+\.\d+$")
        # 生产依赖只剩真正要随包的运行时（原生模块 + ws）
        self.assertEqual(sorted(root["dependencies"]), ["onnxruntime-node", "ws"])

    def test_ort剪枝不写死napi版本目录(self):
        # onnxruntime-node 1.31 起目录由 napi-v3 变成 napi-v6：剪枝必须遍历 bin/ 下实际存在的目录
        text = _read(DESKTOP / "build" / "prune-ort.cjs")
        self.assertNotIn("'napi-v3'", text)
        self.assertIn("fs.readdirSync(root)", text)

    def test_deb依赖声明xdg_utils(self):
        # afterInstall 调 xdg-desktop-menu forceupdate：只判断 command -v 而不声明依赖，
        # 精简系统上菜单要等重新登录才出现
        after = _read(DESKTOP / "build" / "afterInstall.sh")
        self.assertIn("xdg-desktop-menu", after)
        self.assertIn("xdg-utils", self.text)

    def test_卸载钩子按注册路径移除alternative(self):
        """afterInstall 用 /opt 下的真实可执行文件注册 alternatives。

        update-alternatives --remove 的第二个参数必须与 --install 时注册的路径逐字一致，
        写成 /usr/bin 链接会让 dpkg 卸载后 /etc/alternatives 里残留死链。
        """
        after_install = _read(DESKTOP / "build" / "afterInstall.sh")
        after_remove = _read(DESKTOP / "build" / "afterRemove.sh")
        self.assertIn('APP_DIR="/opt/uvmp-toolkit"', after_install)
        self.assertIn('APP_DIR="/opt/uvmp-toolkit"', after_remove)
        self.assertIn(
            'update-alternatives --install "/usr/bin/$EXE_NAME" "$EXE_NAME" "$APP_DIR/$EXE_NAME"',
            after_install,
        )
        self.assertIn('update-alternatives --remove "$EXE_NAME" "$APP_DIR/$EXE_NAME"', after_remove)
        self.assertNotIn('--remove "$EXE_NAME" "/usr/bin/$EXE_NAME"', after_remove)

    def test_便携zip也写version_txt(self):
        """NSIS 的 customInstall 只在安装器里跑，绿色 zip 直接解压 win-unpacked 不会执行它。

        version.txt 只能由 afterPack 钩子写进 appOutDir（zip/tar.gz/目录三种产物共用）。
        """
        self.assertIn("afterPack: build/prune-ort.cjs", self.text)
        hook = _read(DESKTOP / "build" / "prune-ort.cjs")
        self.assertIn("version.txt", hook)
        self.assertIn("appInfo.version", hook)
        # 目标目录必须是 resources/core（与 core/version.py 读的 exe 旁路径一致）
        self.assertIn("'core'", hook)

    def test_afterPack缺核心即失败并写对version_txt(self):
        """用真 node 跑一次 afterPack 钩子，覆盖「缺核心」和「有核心」两条路径。

        electron-builder 对缺失的 extraResources 只打 warn，缺核心的安装包能一路产出到 Release
        （真实踩到过：--dir 打完、应用起不来）。钩子必须在缺核心时抛错阻断打包。
        """
        node = shutil.which("node")
        if node is None:
            self.skipTest("环境中没有 node")
        with tempfile.TemporaryDirectory() as tmp:
            out_dir = Path(tmp) / "win-unpacked"
            (out_dir / "resources" / "core").mkdir(parents=True)
            native = (
                out_dir
                / "resources"
                / "app.asar.unpacked"
                / "node_modules"
                / "onnxruntime-node"
                / "bin"
                / "napi-v6"
                / "win32"
                / "x64"
            )
            native.mkdir(parents=True)
            runtime = Path(tmp) / "msvc-runtime"
            runtime.mkdir()
            for name in (
                "msvcp140.dll",
                "msvcp140_1.dll",
                "msvcp140_atomic_wait.dll",
                "vcruntime140.dll",
                "vcruntime140_1.dll",
            ):
                (runtime / name).write_bytes(b"")
            ctx = {
                "appOutDir": str(out_dir),
                "electronPlatformName": "win32",
                "arch": 1,
                "packager": {"appInfo": {"version": "9.9.9"}},
            }
            ctx_json = json.dumps(ctx)
            script = Path(tmp) / "hook.cjs"
            script.write_text(
                "process.env.UVMP_VC_RUNTIME_DIR = %s;\n"
                "const hook = require(%s);\n"
                "hook.default(%s).then(() => console.log('OK'),"
                " (e) => { console.error(e.message); process.exit(3); });\n"
                % (
                    json.dumps(str(runtime)),
                    json.dumps(str(DESKTOP / "build" / "prune-ort.cjs")),
                    ctx_json,
                ),
                encoding="utf-8",
            )

            # 只有空 core 目录、没有 uvmp-core.exe：必须失败
            missing = subprocess.run(
                [node, str(script)], capture_output=True, text=True, timeout=120
            )
            self.assertEqual(missing.returncode, 3, missing.stdout + missing.stderr)
            self.assertIn("缺少 Python 核心", missing.stderr)

            # 放上可执行文件后：通过并写入 version.txt
            (out_dir / "resources" / "core" / "uvmp-core.exe").write_text("", encoding="utf-8")
            ok = subprocess.run([node, str(script)], capture_output=True, text=True, timeout=120)
            self.assertEqual(ok.returncode, 0, ok.stdout + ok.stderr)
            written = (out_dir / "resources" / "core" / "version.txt").read_text(encoding="utf-8")
            self.assertEqual(written, "9.9.9\n")

    def test_afterPack按macOS的app包结构定位resources(self):
        """macOS 的 resources 在 <Product>.app/Contents/Resources，不在 appOutDir/resources。

        钩子按 Windows 布局判断会把正常产物判成「缺核心」直接失败（已踩到）。
        """
        node = shutil.which("node")
        if node is None:
            self.skipTest("环境中没有 node")
        with tempfile.TemporaryDirectory() as tmp:
            out_dir = Path(tmp) / "mac-arm64"
            resources = out_dir / "Shade 壹匣 - 内网.app" / "Contents" / "Resources"
            (resources / "core").mkdir(parents=True)
            (resources / "core" / "uvmp-core").write_text("", encoding="utf-8")
            ctx = {
                "appOutDir": str(out_dir),
                "electronPlatformName": "darwin",
                "arch": 3,
                "packager": {"appInfo": {"version": "1.2.3"}},
            }
            script = Path(tmp) / "hook.cjs"
            script.write_text(
                "const hook = require(%s);\n"
                "hook.default(%s).then(() => console.log('OK'),"
                " (e) => { console.error(e.message); process.exit(3); });\n"
                % (json.dumps(str(DESKTOP / "build" / "prune-ort.cjs")), json.dumps(ctx)),
                encoding="utf-8",
            )
            out = subprocess.run([node, str(script)], capture_output=True, text=True, timeout=120)
            self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
            self.assertEqual((resources / "core" / "version.txt").read_text(encoding="utf-8"), "1.2.3\n")
            # 不存在 appOutDir/resources 这一层，钩子不能在那里找
            self.assertFalse((out_dir / "resources").exists())


class DependencyOverrideTest(unittest.TestCase):
    """minimatch 的 overrides 必须按「各消费方的调用方式」分组。

    踩过两次坑：
    - 一刀切压到 11：glob@7 的 `this.minimatch.set[...]` 结构变了 → TypeError，electron-builder 打包直接崩
    - 把 @electron/asar 也压到 9/11：asar 用 `__importDefault(require('minimatch')).default(...)`，
      而 minimatch 9+ 的 CJS 导出没有可调用的 default → TypeError
    """

    def setUp(self):
        import json

        self.pkg = json.loads(_read(DESKTOP / "package.json"))
        self.overrides = self.pkg.get("overrides", {})

    def test_asar与glob保留3x(self):
        self.assertEqual(self.overrides["@electron/asar"]["minimatch"], "^3.0.4")
        self.assertEqual(self.overrides["glob"]["minimatch"], "^3.1.1")

    def test_universal与app_builder_lib用11(self):
        self.assertEqual(self.overrides["@electron/universal"]["minimatch"], "^11.0.7")
        self.assertEqual(self.overrides["app-builder-lib"]["minimatch"], "^11.0.7")

    def test_不存在全局一刀切override(self):
        # 键为 minimatch 时会对所有层级生效——正是炸掉 glob@7 的写法
        self.assertNotIn("minimatch", self.overrides)


class DesktopEntryRenderTest(unittest.TestCase):
    """用 electron-builder 自己的 LinuxTargetHelper 渲染 .desktop，确认真实产物。

    schema 只保证「键合法」，不保证「值落到 [Desktop Entry] 里」。desktop.entry 被
    deepAssign 合并进 desktopMeta（LinuxTargetHelper.computeDesktopEntry），渲染一次即可确认
    Name/Comment/StartupWMClass 都在，且 Exec 指向 /opt 下的真实可执行文件。
    """

    def test_desktop文件含Name_Comment_StartupWMClass(self):
        node = shutil.which("node")
        if node is None:
            self.skipTest("环境中没有 node")
        scheme_path = DESKTOP / "node_modules" / "app-builder-lib" / "scheme.json"
        if not scheme_path.is_file():
            self.skipTest("未安装 desktop/node_modules（先 npm ci 再跑本断言）")
        script = DESKTOP / ".uvmp-desktop-render.cjs"
        script.write_text(_DESKTOP_RENDER_JS, encoding="utf-8")
        try:
            out = subprocess.run(
                [node, str(script), str(DESKTOP)],
                capture_output=True,
                text=True,
                timeout=120,
            )
        finally:
            script.unlink(missing_ok=True)
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        entry = out.stdout
        self.assertIn("[Desktop Entry]", entry)
        self.assertIn("Name=Shade 壹匣 - 内网", entry)
        self.assertIn("Comment=派车同步 / 轨迹导出 / 题库搜题", entry)
        self.assertIn("StartupWMClass=uvmp-toolkit", entry)
        self.assertIn("Exec=/opt/uvmp-toolkit/uvmp-toolkit", entry)
        self.assertIn("Type=Application", entry)

    def test_desktop文件含全部图片MIME且用F参数(self):
        """关联声明只有落到 .desktop 的 MimeType 与 MIME XML 里，桌面环境才认。

        `Exec=... %F`（不是默认的 %U）：多选一批图片时系统给的是多个本地路径，
        图片窗口按 argv 直接打开；%U 会变成 file:// URI，需要再解码一次。
        """
        node = shutil.which("node")
        if node is None:
            self.skipTest("环境中没有 node")
        if not (DESKTOP / "node_modules" / "app-builder-lib" / "scheme.json").is_file():
            self.skipTest("未安装 desktop/node_modules（先 npm ci 再跑本断言）")
        script = DESKTOP / ".uvmp-desktop-render.cjs"
        script.write_text(_DESKTOP_RENDER_JS, encoding="utf-8")
        try:
            out = subprocess.run(
                [node, str(script), str(DESKTOP)],
                capture_output=True,
                text=True,
                timeout=120,
            )
        finally:
            script.unlink(missing_ok=True)
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        entry = out.stdout
        self.assertIn("Exec=/opt/uvmp-toolkit/uvmp-toolkit %F", entry)
        mime_line = next(
            (line for line in entry.splitlines() if line.startswith("MimeType=")), ""
        )
        self.assertTrue(mime_line, "desktop 文件缺少 MimeType")
        for mime in _IMAGE_MIMES:
            self.assertIn(mime + ";", mime_line, "desktop MimeType 缺少 %s" % mime)

    def test_mime_xml覆盖全部查看格式(self):
        """deb 会把 fileAssociations 的 mimeType 生成为 /usr/share/mime/packages/*.xml，
        桌面环境据此把扩展名映射到 MIME。渲染一次确认每个扩展名都有 glob。"""
        node = shutil.which("node")
        if node is None:
            self.skipTest("环境中没有 node")
        if not (DESKTOP / "node_modules" / "app-builder-lib" / "scheme.json").is_file():
            self.skipTest("未安装 desktop/node_modules（先 npm ci 再跑本断言）")
        script = DESKTOP / ".uvmp-mime-render.cjs"
        script.write_text(_MIME_RENDER_JS, encoding="utf-8")
        try:
            out = subprocess.run(
                [node, str(script), str(DESKTOP)],
                capture_output=True,
                text=True,
                timeout=120,
            )
        finally:
            script.unlink(missing_ok=True)
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        xml = out.stdout
        for ext in _IMAGE_EXTS:
            self.assertIn('<glob pattern="*.%s"/>' % ext, xml, "MIME XML 缺少 .%s" % ext)
        for mime in _IMAGE_MIMES:
            self.assertIn('<mime-type type="%s">' % mime, xml, "MIME XML 缺少 %s" % mime)


# Linux 侧每个扩展名单写成一项：electron-builder 把 ext 原样拼进
# `<glob pattern="*.${ext}"/>`，`[jpg, jpeg]` 会变成非法的 `*.jpg,jpeg`。
# .jpeg / .tiff 这类别名由 shared-mime-info 自带的扩展名映射覆盖
# （image/jpeg 含 *.jpeg、*.jpg；image/tiff 含 *.tif、*.tiff），不必重复登记。
_IMAGE_EXTS = (
    "avif",
    "bmp",
    "gif",
    "ico",
    "jpg",
    "png",
    "tif",
    "webp",
)
_IMAGE_MIMES = (
    "image/jpeg",
    "image/png",
    "image/gif",
    "image/webp",
    "image/bmp",
    "image/vnd.microsoft.icon",
    "image/avif",
    "image/tiff",
)


_MIME_RENDER_JS = """\
// 用 electron-builder 的 LinuxTargetHelper 渲染 MIME XML（不打包、不联网）。
const fs = require('fs');
const path = require('path');
const os = require('os');
const base = process.argv[2];
const yaml = require(path.join(base, 'node_modules', 'js-yaml'));
const { LinuxTargetHelper } = require(
  path.join(base, 'node_modules', 'app-builder-lib', 'out', 'targets', 'LinuxTargetHelper.js')
);
const cfg = yaml.load(fs.readFileSync(path.join(base, 'electron-builder.yml'), 'utf8'));
const packager = {
  appInfo: { productName: 'Shade 壹匣 - 内网', sanitizedProductName: 'uvmp-toolkit', version: '0.0.0' },
  executableName: 'uvmp-toolkit',
  fileAssociations: [].concat(cfg.linux.fileAssociations || []),
  config: cfg,
  platformSpecificBuildOptions: cfg.linux,
  info: { metadata: { desktopName: 'uvmp-toolkit.desktop' } },
  getTempFile: async name => path.join(os.tmpdir(), 'uvmp-mime-' + name),
};
Promise.resolve(new LinuxTargetHelper(packager).mimeTypeFiles)
  .then(file => process.stdout.write(file ? fs.readFileSync(file, 'utf8') : ''),
        err => { console.error(err); process.exit(1); });
"""


_DESKTOP_RENDER_JS = """\
// 用 electron-builder 的 LinuxTargetHelper 渲染一次 .desktop（不打包、不联网）。
const fs = require('fs');
const path = require('path');
const base = process.argv[2];
const yaml = require(path.join(base, 'node_modules', 'js-yaml'));
const { LinuxTargetHelper } = require(
  path.join(base, 'node_modules', 'app-builder-lib', 'out', 'targets', 'LinuxTargetHelper.js')
);
const cfg = yaml.load(fs.readFileSync(path.join(base, 'electron-builder.yml'), 'utf8'));
const packager = {
  appInfo: { productName: 'Shade 壹匣 - 内网', sanitizedProductName: 'uvmp-toolkit', version: '0.0.0' },
  executableName: 'uvmp-toolkit',
  // 关联声明必须喂进 packager：.desktop 的 MimeType 由 fileAssociations 汇总而来
  fileAssociations: [].concat(cfg.linux.fileAssociations || []),
  config: cfg,
  platformSpecificBuildOptions: cfg.linux,
  info: { metadata: { desktopName: 'uvmp-toolkit.desktop' } },
  getTempFile: async name => path.join(require('os').tmpdir(), 'uvmp-desktop-' + name),
};
Promise.resolve(new LinuxTargetHelper(packager).computeDesktopEntry(cfg.linux, undefined, {}))
  .then(text => process.stdout.write(text), err => { console.error(err); process.exit(1); });
"""


class ReleaseChainTest(unittest.TestCase):
    def test_发布生成并校验SHA256SUMS(self):
        text = _read(REPO / ".github" / "workflows" / "release.yml")
        self.assertIn("SHA256SUMS", text)
        self.assertIn("sha256sum -c", text)

    def test_校验和来自构建端而非下载后自算(self):
        """下载后再自己算哈希 = 自证循环，上传截断永远发现不了。

        正确做法：三个 client job 在本地构件上算 *.sha256 随附件上传，
        发布段下载后据清单校验、合并成 SHA256SUMS 再回读比对。
        """
        release = _read(REPO / ".github" / "workflows" / "release.yml")
        # Windows 1 个 + linux amd64/arm64 各 1 个，共 3 处「本地算哈希」
        self.assertEqual(release.count('sha256sum "${exe[@]}" > "${exe[0]}.sha256"'), 1)
        self.assertEqual(release.count('sha256sum "${debs[@]}" > "${debs[0]}.sha256"'), 2)
        # 三份哈希都要随附件上传
        self.assertEqual(release.count("dist-electron/*.sha256"), 3)

        for name in ("release.yml", "release-publish.yml"):
            text = _read(REPO / ".github" / "workflows" / name)
            self.assertIn("per_job=( *.sha256 )", text, name)
            self.assertIn("sha256sum -c SHA256SUMS", text, name)
            # 绝不允许对「下载下来的安装包」重新生成清单（自证循环）
            self.assertNotIn("sha256sum ./*.exe ./*.deb", text, name)

    def test_构建job内的相对路径与working_directory一致(self):
        """client-* job 的 defaults.run.working-directory 是 uvmp-toolkit，
        脚本里再写 cd uvmp-toolkit/... 就会变成嵌套路径、直接失败。"""
        text = _read(REPO / ".github" / "workflows" / "release.yml")
        self.assertIn("working-directory: uvmp-toolkit", text)
        self.assertNotIn("cd uvmp-toolkit/dist-electron", text)
        self.assertEqual(text.count("cd dist-electron"), 3)

    def test_校验步骤不使用ls管道计数(self):
        # `files=$(ls ... | wc -l)` 在 set -euo pipefail 且无匹配时会静默中止（且计数含空格）
        for name in ("release.yml", "release-publish.yml"):
            text = _read(REPO / ".github" / "workflows" / name)
            self.assertNotIn("ls -1 ./*.exe", text, name)

    def test_CNB接力校验SHA256SUMS(self):
        text = _read(REPO / "docker" / "cnb-relay-release-assets.sh")
        self.assertIn("SHA256SUMS", text)
        self.assertIn("sha256sum -c", text)
        self.assertIn('DEB_ARM+=("$f")', text)

    def test_附件命名沿用正式三件口径(self):
        # 网关/文档/取件脚本都按这套名字断言，改 electron-builder 时不能只改一处
        cfg = _read(DESKTOP / "electron-builder.yml")
        self.assertIn("artifactName: uvmp-toolkit-Setup-${version}.${ext}", cfg)
        self.assertIn("artifactName: uvmp-toolkit_${version}_${arch}.${ext}", cfg)
        relay = _read(REPO / "docker" / "cnb-relay-release-assets.sh")
        for pat in (
            "uvmp-toolkit-Setup-*.exe",
            "uvmp-toolkit_*_amd64.deb",
            "uvmp-toolkit_*_arm64.deb",
        ):
            self.assertIn(pat, relay)

    def test_不再回退公共代理(self):
        for path in (
            REPO / "docker" / "cnb-relay-release-assets.sh",
            REPO / ".github" / "workflows" / "release.yml",
            REPO / ".cnb.yml",
        ):
            text = _read(path)
            # 注释里可以提到，但不能真的拼进下载 URL
            self.assertNotRegex(text, r"URLS\+=\(\"https://gh", str(path))
            self.assertNotIn("ghfast.top/https", text, str(path))

    def test_三个client_job都跳过onnx_cuda下载(self):
        """onnxruntime-node 的 postinstall 会按需拉 CUDA 运行库，本项目只用 CPU EP。

        windows job 与 linux arm64 job 都设了 ONNXRUNTIME_NODE_INSTALL_CUDA=skip，
        linux amd64 漏设 → 该架构的 npm ci 会多下载数百 MB CUDA 包（且离线 runner 上直接失败）。
        """
        text = _read(REPO / ".github" / "workflows" / "release.yml")
        for job in ("client-windows", "client-linux-amd64", "client-linux-arm64"):
            block = _job_block(text, job)
            self.assertIn("ONNXRUNTIME_NODE_INSTALL_CUDA: skip", block, job)


class GuiSmokeGateTest(unittest.TestCase):
    """图片工具 / 批量重命名的真实 GUI 冒烟必须进本地出包与 Windows Release 门禁。

    两个冒烟都会拉起完整 Electron 应用、走真实核心 RPC 并写临时样本；只写在开发指南里
    等于没有回归保护（review-verification-fixes.md 的 M-7）。它们把 UVMP_PYTHON 当首选
    解释器，还要 import PIL 生成样本：Windows 上的 `python` 可能是 App Execution Alias，
    隔离 APPDATA 后解析不到运行时（核心直接起不来），所以门禁必须先解析/校验绝对解释器。
    """

    SMOKES = ("test/image-tools-gui-smoke.cjs", "test/batch-rename-gui-smoke.cjs")
    LOCAL_START = "== 门禁：图片 / 批量重命名 GUI 冒烟 =="
    LOCAL_END = "# ---------- 4) Linux 打包"
    CI_START = "GUI 冒烟（图片工具 / 批量重命名"
    CI_END = "- name: 记录安装包 SHA256"

    @staticmethod
    def _clip(text, start, end):
        begin = text.find(start)
        if begin < 0:
            raise AssertionError("找不到区块起点：%s" % start)
        finish = text.find(end, begin)
        if finish < 0:
            raise AssertionError("找不到区块终点：%s" % end)
        return text[begin:finish]

    def _assert_both_smokes_block(self, block, label):
        image_at = block.find(self.SMOKES[0])
        batch_at = block.find(self.SMOKES[1])
        self.assertGreaterEqual(image_at, 0, "%s 未运行 %s" % (label, self.SMOKES[0]))
        self.assertGreaterEqual(batch_at, 0, "%s 未运行 %s" % (label, self.SMOKES[1]))
        self.assertLess(image_at, batch_at, "%s 必须先跑图片冒烟再跑批量冒烟" % label)
        for name in self.SMOKES:
            tail = block[block.find(name) : block.find(name) + 400]
            with self.subTest(script=name):
                self.assertRegex(
                    tail, r"LASTEXITCODE\s+-ne\s+0", "%s 未判 %s 的退出码" % (label, name)
                )
                self.assertIn("throw ", tail, "%s 的 %s 失败必须阻断门禁" % (label, name))

    def test_本地出包在两个GUI冒烟上阻断(self):
        local = _read(PACKAGING / "build_local.ps1")
        block = self._clip(local, self.LOCAL_START, self.LOCAL_END)
        self._assert_both_smokes_block(block, "build_local.ps1")
        # 必须排在主窗口冒烟之后：主窗口自检跑完 process.exit(0)，两者不能抢同一进程
        self.assertLess(
            local.find('$env:UVMP_CI_SMOKE = "1"'),
            local.find(self.LOCAL_START),
            "两个 GUI 冒烟必须放在主窗口冒烟之后",
        )
        # 冒烟按相对 cwd 找 electron/dist/node_modules，必须在 desktop/ 下执行
        self.assertLess(
            block.find("Push-Location desktop"),
            block.find(self.SMOKES[0]),
            "跑冒烟前必须先切到 desktop/",
        )
        # 解释器：本机先由当前解释器解析出 sys.executable 绝对路径，并校验 Pillow
        self.assertIn("sys.executable", block, "build_local.ps1 未解析绝对解释器")
        self.assertIn("$env:UVMP_PYTHON =", block, "build_local.ps1 未设置 UVMP_PYTHON")
        self.assertNotRegex(
            block,
            r'UVMP_PYTHON\s*=\s*["\']python3?["\']',
            "build_local.ps1 不能把系统 python 别名交给冒烟",
        )
        self.assertRegex(block, r"import\s+(sys,\s*)?PIL", "未校验 Pillow（生成临时样本必需）")

    def test_Windows_Release在两个GUI冒烟上阻断(self):
        ci = _read(REPO / ".github" / "workflows" / "release.yml")
        job = _job_block(ci, "client-windows")
        block = self._clip(job, self.CI_START, self.CI_END)
        self._assert_both_smokes_block(block, "release.yml")
        # 顺序：主窗口冒烟 → 两个真实窗口冒烟 → 记录安装包 SHA256
        self.assertLess(
            job.find("GUI 冒烟（UVMP_CI_SMOKE"),
            job.find(self.CI_START),
            "新冒烟必须排在主窗口冒烟之后",
        )
        # CI 只认已校验的内置 CPython（PY315），不用系统 python 别名
        self.assertRegex(block, r"\$env:UVMP_PYTHON\s*=\s*\$env:PY315")
        self.assertRegex(block, r'if\s*\(\s*-not\s+\$env:PY315\s*\)', "PY315 缺失必须显式失败")
        self.assertNotRegex(
            block,
            r'UVMP_PYTHON\s*=\s*["\']python3?["\']',
            "release.yml 不能把系统 python 别名交给冒烟",
        )
        # 相对路径：job 的工作目录已是 uvmp-toolkit，冒烟要在 desktop/ 下跑，不能再拼一层
        self.assertLess(
            block.find("Push-Location desktop"), block.find(self.SMOKES[0]), "先切到 desktop/"
        )
        self.assertNotIn("node desktop\\test", block, "路径已相对 uvmp-toolkit，不应再拼 desktop")


class ScriptEncodingGuardTest(unittest.TestCase):
    """Windows 批处理脚本首字节不得是 UTF-8 BOM：`cmd` 会把 `﻿@echo off` 当成未知命令，
    首行报错 + 之后不再静默回显，内网用户常据此判断「安装失败」。

    PowerShell 脚本不受此限（`build_local.ps1` 的 BOM 是 BASE 就有的，PowerShell 能正确处理），
    因此只守护 `*.bat` / `*.cmd`。
    """

    def test_批处理脚本无BOM且以命令起始(self):
        scripts = sorted(ROOT.glob("install/**/*.bat")) + sorted(ROOT.glob("install/**/*.cmd"))
        scripts += sorted(ROOT.glob("packaging/**/*.bat")) + sorted(
            ROOT.glob("packaging/**/*.cmd")
        )
        self.assertTrue(scripts, "没有找到任何 .bat/.cmd 脚本，守卫范围可疑")
        for path in scripts:
            with self.subTest(script=str(path)):
                head = path.read_bytes()[:4]
                self.assertNotEqual(
                    b"\xef\xbb\xbf",
                    head[:3],
                    "%s 带 UTF-8 BOM，cmd 首行会报错" % path.name,
                )
                # BOM 之后的第一个字符必须是 @ 或 : （标签）或 rem，不能是乱码
                text = path.read_bytes().decode("utf-8")
                self.assertRegex(
                    text.lstrip()[:1], r"[@:rR]", "%s 首字符应为 @ 或 :" % path.name
                )

    def test_批处理脚本使用CRLF换行(self):
        for path in sorted(ROOT.glob("install/**/*.bat")):
            with self.subTest(script=str(path)):
                data = path.read_bytes()
                # 中文内容按 UTF-8 保存（脚本自身 chcp 65001），换行保持 CRLF 以匹配 Windows 编辑工具
                self.assertIn(b"\r\n", data, "%s 应使用 CRLF" % path.name)
                text = data.decode("utf-8")
                self.assertTrue(
                    any(ord(ch) > 127 for ch in text), "%s 中文内容不应丢失" % path.name
                )


class RpcPingGateTest(unittest.TestCase):
    """rpc ping 门禁失败时必须留下真实响应与退出码。

    实测过一次「selfcheck 通过、rpc ping 失败」但日志里只有一行 throw：门禁只比较了
    文本，没有保留子进程实际输出，连「空响应」还是「报错响应」都分不出来，定位只能靠重跑。
    空响应的成因之一已修在 toolkit/rpc.py（请求行带 UTF-8 BOM 时整行被丢弃），
    这里的断言保证下次出问题能直接看到现场。
    """

    GATES = (
        PACKAGING / "build_local.ps1",
        REPO / ".github" / "workflows" / "release.yml",
    )
    # Unix 侧同构门禁：同样必须保留原始响应并判退出码
    UNIX_GATES = (
        PACKAGING / "build_unix.sh",
        PACKAGING / "build_on_device.sh",
    )

    def test_失败时打印原始响应与退出码(self):
        for path in self.GATES:
            text = _read(path)
            self.assertIn("$pingCode = $LASTEXITCODE", text, path.name)
            self.assertIn("rpc ping 原始响应（exit=$pingCode）", text, path.name)
            # 旧写法：直接拿原始管道变量做文本匹配，失败信息里不含子进程输出
            self.assertNotIn(
                "-not ($ping -match",
                text,
                "%s 的 rpc ping 门禁仍只比较文本" % path.name,
            )

    def test_退出码非零也算失败(self):
        """纯文本匹配会放过「打印了 ok 但进程异常退出」的情形。"""
        for path in self.GATES:
            text = _read(path)
            self.assertIn("$pingCode -ne 0 -or -not ($pingText -match", text, path.name)

    def test_Unix侧同样保留原始响应并判退出码(self):
        """Unix 两处原来只有 `| grep -q`：失败时日志里没有任何现场。"""
        for path in self.UNIX_GATES:
            text = _read(path)
            with self.subTest(script=path.name):
                self.assertNotRegex(
                    text,
                    r"rpc \| grep -q '\"ok\": true'",
                    "%s 的 rpc ping 仍是纯 grep 门禁" % path.name,
                )
                self.assertIn("ping_code", text, "%s 未记录退出码" % path.name)
                self.assertIn("原始响应", text, "%s 未打印原始响应" % path.name)
                # 组合条件：退出码非零 或 响应不含 ok
                self.assertIn('ping_code" -ne 0', text, path.name)


class BuildOnDeviceTest(unittest.TestCase):
    """离线构建脚本的「现代 / 旧兼容」分界必须与 requirements-core.txt 的最低 Python 一致。

    依赖里最高的下限是图片引擎 Pillow 13.0.0（要求 3.11+）；requests 2.34 / pypdf 7 /
    pyinstaller 6 要求 3.10+。分界低于真实下限会让装不上依赖的解释器走现代路径、
    pip 找不到可用 wheel 后整锅失败；高于下限则会把能装的机器误判成旧兼容分支。
    """

    def setUp(self):
        self.text = _read(PACKAGING / "build_on_device.sh")

    def test_分界为3_11(self):
        self.assertIn("sys.version_info < (3, 11)", self.text)
        self.assertNotIn("sys.version_info < (3, 10)", self.text)
        self.assertNotIn("sys.version_info < (3, 9)", self.text)
        # 旧兼容提示语也要跟着改，免得误导现场按 3.9 准备解释器
        self.assertNotIn("Python < 3.9", self.text)

    def test_旧兼容分支仍钉死依赖(self):
        # 旧解释器路径不许裸包名（会把 pip 解析到装不上的最新版）
        m = re.search(r"BUILD_DEPS=\((.*?)\)", self.text, re.S)
        self.assertIsNotNone(m)
        for pkg in m.group(1).split():
            if pkg.endswith("\\"):
                continue
            self.assertIn("==", pkg, pkg)


class DesktopLockfileTest(unittest.TestCase):
    """package-lock.json 必须能直接支撑 npm ci。

    缺 resolved/integrity 的条目会让 npm ci 逐包回 registry 解析元数据（国内网络下表现为
    长时间卡住或失败），也让「锁文件即完整性清单」这一前提失效。
    """

    def setUp(self):
        import json

        self.lock = json.loads(_read(DESKTOP / "package-lock.json"))
        self.pkgs = {
            k: v for k, v in self.lock["packages"].items() if k and not v.get("link")
        }

    def test_每个registry包都带resolved与integrity(self):
        self.assertEqual(self.lock["lockfileVersion"], 3)
        self.assertGreater(len(self.pkgs), 300, "包数量异常，锁文件可能被裁剪")
        missing = [
            k for k, v in self.pkgs.items() if not v.get("resolved") or not v.get("integrity")
        ]
        self.assertEqual(missing, [], "缺 resolved/integrity：%s" % missing[:10])

    def test_resolved全部指向npm官方registry(self):
        bad = [
            k
            for k, v in self.pkgs.items()
            if not v["resolved"].startswith("https://registry.npmjs.org/")
        ]
        self.assertEqual(bad, [], bad[:10])
        for k, v in self.pkgs.items():
            self.assertRegex(v["integrity"], r"^sha512-", k)

    def test_锁文件与package_json同步且目标版本未漂移(self):
        import json

        pkg = json.loads(_read(DESKTOP / "package.json"))
        root = self.lock["packages"][""]
        self.assertEqual(root["version"], pkg["version"])
        self.assertEqual(root["dependencies"], pkg["dependencies"])
        self.assertEqual(root["devDependencies"], pkg["devDependencies"])
        # Electron 45.3.0 / electron-builder 26.18.0 / onnxruntime-node 1.31.1 是发版基线
        self.assertEqual(self.pkgs["node_modules/electron"]["version"], "45.3.0")
        self.assertEqual(self.pkgs["node_modules/electron-builder"]["version"], "26.18.0")
        self.assertEqual(self.pkgs["node_modules/onnxruntime-node"]["version"], "1.31.1")


def _shell_function(text, name, script):
    """取出 bash 里 name() { ... } 的函数体（用于在测试里重放装配断言）。"""
    m = re.search(r"^%s\(\) \{\n(.*?)^\}$" % re.escape(name), text, re.S | re.M)
    if m is None:
        raise AssertionError("%s 中找不到函数 %s()" % (script, name))
    return m.group(0)


def _powershell_function(text, name):
    """取出 PowerShell 里 function Name(...) { ... } 的完整定义。"""
    m = re.search(r"^function %s\(.*?\n\}" % re.escape(name), text, re.S | re.M)
    if m is None:
        raise AssertionError("build_local.ps1 中找不到函数 %s()" % name)
    return m.group(0)


def _touch(path):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("", encoding="utf-8")


def _write_sh(bash, path, content):
    """写 shell 脚本并置可执行位：Windows 上 Python 的 chmod 不会真正设 x 位，交给 bash 做。"""
    path.write_text(content, encoding="utf-8")
    subprocess.run([bash, "-c", 'chmod +x "$1"', "bash", str(path)], check=True)
    return path


def _core_modules_probe(bash, tmp, func_body):
    """构造一个可重复调用的探针：给定 (playwright, imaging, 归档清单)，跑一遍 assert_core_modules。

    清单写进一个假的 `$PY`（只打印 archive_viewer 的内容），避免依赖真实 PyInstaller。
    """
    tmp = Path(tmp)
    root = tmp / "uvmp-core"
    root.mkdir(exist_ok=True)
    _write_sh(bash, root / "uvmp-core", "#!/bin/bash\nexit 0\n")
    fake = tmp / "fake-python"
    listing = tmp / "listing.txt"
    listing.write_text("", encoding="utf-8")
    _write_sh(bash, fake, '#!/bin/bash\ncat "$UVMP_FAKE_LISTING"\n')
    probe = tmp / "probe.sh"
    probe.write_text(
        "set -u\nPY=%s\nUVMP_FAKE_LISTING=%s\nexport UVMP_FAKE_LISTING\n%s\n"
        'assert_core_modules %s uvmp-core "$1" "$2"\n'
        % (_sh_quote(str(fake)), _sh_quote(str(listing)), func_body, _sh_quote(str(root))),
        encoding="utf-8",
    )

    def run(playwright, imaging, modules):
        listing.write_text(modules, encoding="utf-8")
        return subprocess.run(
            [bash, str(probe), str(playwright), str(imaging)], capture_output=True, text=True
        )

    return run


def _strip_comments(text, marker):
    """去掉整行注释：注释里解释「为什么不碰某物」是必要的，静态门禁只看真正的代码行。"""
    return "\n".join(
        line for line in text.splitlines() if line.strip() and not line.strip().startswith(marker)
    )


def _run_shell_payload(bash, workdir, func, root, rel):
    """在 bash 里调用定位函数：命中返回路径，未命中返回 None。"""
    script = Path(workdir) / "probe.sh"
    script.write_text(
        "set -u\n%s\ncore_payload_path %s %s\n"
        % (func, _sh_quote(str(root)), _sh_quote(str(rel))),
        encoding="utf-8",
    )
    proc = subprocess.run([bash, str(script)], capture_output=True, text=True)
    out = proc.stdout.strip()
    if proc.returncode == 0 and out:
        return out
    if proc.returncode == 1 and not out:
        return None
    raise AssertionError("定位函数异常退出 rc=%s out=%r err=%r" % (proc.returncode, out, proc.stderr))


def _norm_path(value):
    """把 shell 打印的路径（可能是正斜杠）与 pathlib 路径统一成可比较的形式。

    只做分隔符归一 + 解析符号相对段，不改变「必须是精确路径」的断言强度。
    """
    if value is None:
        return None
    return os.path.normcase(os.path.realpath(str(value)))


def _sh_quote(value):
    return "'" + value.replace("'", "'\\''") + "'"


def _run_pwsh_payload(pwsh, workdir, func, root, rel):
    script = Path(workdir) / "probe.ps1"
    script.write_text(
        "%s\n$p = Get-CorePayloadPath '%s' '%s'\nif ($p) { Write-Output $p }\n"
        % (func, root, rel),
        encoding="utf-8",
    )
    proc = subprocess.run([pwsh, "-NoProfile", "-File", str(script)], capture_output=True, text=True)
    if proc.returncode != 0:
        raise AssertionError("pwsh 探针失败 rc=%s err=%r" % (proc.returncode, proc.stderr))
    return proc.stdout.strip()


def _yaml_block(text, key, parent):
    """取 parent 块下 key 的子块键名集合（只按缩进判断，不引第三方 YAML 依赖）。

    electron-builder.yml 是 2 空格缩进；`linux:` → `desktop:` → `entry:` 都在同一层。
    """
    lines = text.splitlines()
    parent_re = re.compile(r"^( *)%s:\s*$" % re.escape(parent))
    start = None
    for i, line in enumerate(lines):
        m = parent_re.match(line)
        if m:
            start = i
            parent_indent = len(m.group(1))
            break
    if start is None:
        return {}
    key_re = re.compile(r"^( *)%s:\s*$" % re.escape(key))
    for j in range(start + 1, len(lines)):
        line = lines[j]
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if len(line) - len(line.lstrip()) <= parent_indent:
            return {}
        m = key_re.match(line)
        if not m:
            continue
        key_indent = len(m.group(1))
        keys = {}
        for k in range(j + 1, len(lines)):
            sub = lines[k]
            if not sub.strip() or sub.lstrip().startswith("#"):
                continue
            sub_indent = len(sub) - len(sub.lstrip())
            if sub_indent <= key_indent:
                break
            if sub_indent == key_indent + 2:
                km = re.match(r"^\s+([A-Za-z0-9_.-]+):", sub)
                if km:
                    keys[km.group(1)] = True
        return keys
    return {}


_SCHEMA_CHECK_JS = """\
// 用 electron-builder 自己的 scheme.json + ajv 校验配置（与打包时同一条路径）。
// 由 packaging/test_build_config.py 临时写到 desktop/ 下，跑完即删（needs 项目根才能解析 js-yaml）。
const fs = require('fs');
const path = require('path');
const [configPath, schemePath] = process.argv.slice(2);
const appLibDir = path.dirname(schemePath); // node_modules/app-builder-lib
const nodeModules = path.dirname(appLibDir);
const yaml = require(path.join(nodeModules, 'js-yaml'));
const { validateSchema } = require(path.join(appLibDir, 'out', 'util', 'config', 'schemaValidator.js'));
const config = yaml.load(fs.readFileSync(configPath, 'utf8'));
const scheme = JSON.parse(fs.readFileSync(schemePath, 'utf8'));
validateSchema(scheme, config, { name: path.basename(configPath) });
"""


def _job_block(text, job):
    """取 GitHub Actions workflow 里某个 job 的正文（2 空格缩进的键为分界）。"""
    m = re.search(
        r"^  %s:\n(.*?)(?=^  [A-Za-z0-9_-]+:\n|\Z)" % re.escape(job),
        text,
        re.S | re.M,
    )
    if m is None:
        raise AssertionError("workflow 中找不到 job: %s" % job)
    return m.group(1)


class OfflinePackagesTest(unittest.TestCase):
    def test_三平台pycryptodomex_wheel齐全(self):
        names = [p.name for p in (ROOT / "offline_packages").glob("pycryptodomex-*.whl")]
        self.assertTrue(any("win_amd64" in n for n in names), names)
        self.assertTrue(any("manylinux2014_x86_64" in n for n in names), names)
        self.assertTrue(any("aarch64" in n for n in names), names)

    def test_离线包无已知高危旧版requests(self):
        # requests 2.27.1 存在代理凭据泄漏（CVE-2023-32681）；2.31.0 是最后支持 Python 3.7 的修复版
        names = [p.name for p in (ROOT / "offline_packages").glob("requests-*.whl")]
        self.assertEqual(len(names), 1, names)
        self.assertIn("requests-2.31.0", names[0])
        # 源码模式旧解释器路径也要跟着走同一个版本
        self.assertIn("requests==2.31.0", _read(PACKAGING / "build_on_device.sh"))

    def test_安装脚本探测gmssl_sm2(self):
        # 只探测 `import gmssl` 会漏掉 Cryptodome 缺失（SM2 加密时才炸），必须探测到 sm2 模块
        for path in (
            ROOT / "install" / "linux" / "install.sh",
            ROOT / "install" / "windows" / "install.bat",
        ):
            text = _read(path)
            self.assertIn("gmssl.sm2", text, str(path))
            self.assertIn("Cryptodome.Util.asn1", text, str(path))

    def test_windows安装脚本解压纯Python轮与win轮(self):
        """同名 .pyd/.so 不能被异构轮覆盖，但纯 Python 轮（none-any）必须解。

        曾是白名单 `findstr win32/win_amd64/win_arm64`：requests、gmssl、pypdf、websocket-client
        全是 none-any，被整批跳过 → 装完 pylib 里只有 pycryptodomex，探测必然失败。
        """
        bat = _read(ROOT / "install" / "windows" / "install.bat")
        self.assertNotRegex(bat, r"findstr[^\n]*win_amd64", "install.bat 不能用 win 白名单过滤 wheel")
        for tag in ("manylinux", "musllinux", "macosx"):
            self.assertIn(tag, bat, "install.bat 应只跳过非 Windows 平台的二进制轮：%s" % tag)

    def test_windows解压规则对现有wheel清单的实际判定(self):
        """把 install.bat 的 findstr 规则重放一遍：纯 Python 轮与 win 轮必须留下，linux 轮必须跳过。"""
        skip_tags = ("manylinux", "musllinux", "linux_", "macosx")
        kept, skipped = [], []
        for whl in sorted((ROOT / "offline_packages").glob("*.whl")):
            (skipped if any(t in whl.name for t in skip_tags) else kept).append(whl.name)
        self.assertTrue(any("none-any" in n for n in kept), kept)
        self.assertTrue(any("win_amd64" in n for n in kept), kept)
        self.assertFalse([n for n in kept if "manylinux" in n or "macosx" in n], kept)
        # 三个必需运行期包的纯 Python 轮必须在 kept 里，否则装完 pylib 里就没有它们
        for prefix in ("requests-", "gmssl-", "pypdf-", "websocket_client-", "urllib3-", "idna-"):
            self.assertTrue([n for n in kept if n.startswith(prefix)], "缺失纯 Python 轮：%s" % prefix)
        self.assertEqual([n for n in skipped if "win" in n], [], skipped)


class ImageToolsPackagingTest(unittest.TestCase):
    """图片工具（Pillow）的构建/安装契约。

    盯住四件容易漏的事：
    - Pillow 版本钉死 13.0.0（requirements-core.txt 是唯一来源，禁止 latest / 区间）；
    - 三个平台的构建脚本与 CI 都用 `--collect-all PIL` 收编，装配断言里也查 PIL
      （漏收只有真跑图片工具才炸，import 阶段看不出）；
    - offline_packages/ 只放三平台 cp315 二进制轮（Windows x64 / manylinux x86_64 / aarch64），
      不夹带 musllinux、macOS、iOS、free-threaded（cp315t）等用不上的轮子；
    - 安装器不静默改写系统默认关联：Linux 只在用户点击时由程序调 xdg-mime，卸载只清自己的项。
    """

    SCRIPTS = ("build_unix.sh", "build_on_device.sh", "build_local.ps1")
    PILLOW_WHEELS = (
        "pillow-13.0.0-cp315-cp315-manylinux_2_27_aarch64.manylinux_2_28_aarch64.whl",
        "pillow-13.0.0-cp315-cp315-manylinux_2_27_x86_64.manylinux_2_28_x86_64.whl",
        "pillow-13.0.0-cp315-cp315-win_amd64.whl",
    )

    def test_requirements钉死Pillow(self):
        lines = [
            ln.strip()
            for ln in _read(PACKAGING / "requirements-core.txt").splitlines()
            if ln.strip() and not ln.strip().startswith("#")
        ]
        pillow = [ln.lower() for ln in lines if ln.lower().startswith("pillow")]
        self.assertEqual(pillow, ["pillow==13.0.0"], "Pillow 必须固定 13.0.0：%s" % pillow)

    def test_requirements不夹带额外依赖(self):
        lines = _read(PACKAGING / "requirements-core.txt").lower()
        # Pillow 本身无运行期依赖；出现 olefile 之类说明顺手加了不该加的东西
        for extra in ("olefile==", "numpy==", "ghostscript"):
            self.assertNotIn(extra, lines, extra)

    def test_构建脚本collect_all_PIL(self):
        for name in self.SCRIPTS:
            self.assertIn("--collect-all PIL", _read(PACKAGING / name), name)
        workflow = _read(REPO / ".github" / "workflows" / "release.yml")
        self.assertIn("--collect-all PIL", workflow)

    def test_装配断言引用同一份模块清单(self):
        """build_unix / build_on_device 的装配断言必须真的查归档模块（语义由重放用例覆盖），
        这里只钉住「清单里确实写了 PIL」的静态事实，并确保 PIL 不挂错分支。"""
        for name in ("build_unix.sh", "build_on_device.sh"):
            body = _shell_function(_read(PACKAGING / name), "assert_core_bundle", name)
            self.assertRegex(
                body, r'assert_core_modules|mods="[^"]*PIL', "%s 的装配断言未覆盖 PIL" % name
            )
        # build_on_device 的 PIL 由独立开关控制，不能写成 playwright 分支里的追加
        dev = _shell_function(
            _read(PACKAGING / "build_on_device.sh"), "assert_core_modules", "build_on_device.sh"
        )
        self.assertIn('[ "$with_imaging" = "1" ]', dev, "PIL 必须由 with_imaging 独立控制")
        playwright_lines = [ln for ln in dev.splitlines() if 'with_playwright" = "1"' in ln]
        self.assertTrue(playwright_lines, "找不到 playwright 分支")
        for line in playwright_lines:
            self.assertNotIn("PIL", line, "PIL 不得挂在 playwright 分支上：%s" % line.strip())

    def test_离线wheel只含三平台cp315(self):
        names = sorted(p.name for p in (ROOT / "offline_packages").glob("pillow-*.whl"))
        self.assertEqual(names, sorted(self.PILLOW_WHEELS))

    def test_离线wheel确实是cp315二进制轮(self):
        for name in self.PILLOW_WHEELS:
            self.assertIn("cp315-cp315", name, name)
            self.assertNotIn("cp315t", name, "free-threaded 轮用不上：%s" % name)
            for tag in ("musllinux", "macosx", "iphonesimulator", "win_arm64"):
                self.assertNotIn(tag, name, "%s 不属于三平台目标：%s" % (tag, name))

    def test_deb不安装Ghostscript(self):
        yml = _read(DESKTOP / "electron-builder.yml")
        self.assertNotIn("ghostscript", yml.lower())
        for name in ("afterInstall.sh", "afterRemove.sh"):
            self.assertNotIn("ghostscript", _read(DESKTOP / "build" / name).lower(), name)

    # ---- 文件关联：只登记「打开方式」，绝不改系统默认应用 ----
    # 判断「有没有改写扩展名默认 ProgID」的正则曾经写成 `Software\\\\Classes\\\\\\.`（要求两个
    # 反斜杠），而 NSIS 脚本里只有一个 → 门禁永远通过、形同虚设。这里抽成模块级函数，
    # 既用来检查真实脚本，也用真实坏写法做反例自检。

    def test_安装不静默改写默认关联(self):
        """Windows 由系统设置掌控默认应用，Linux 只允许用户在程序里点击后改。"""
        after_install = _strip_comments(_read(DESKTOP / "build" / "afterInstall.sh"), "#")
        self.assertNotIn("xdg-mime", after_install, "安装脚本不得替用户改默认关联")
        nsh = _strip_comments(_read(DESKTOP / "build" / "installer.nsh"), ";")
        self.assertNotIn("UserChoice", nsh, "安装器不得改写 UserChoice")
        self.assertNotIn("FileExts", nsh, "安装器不得触碰 FileExts（UserChoice 所在分支）")
        self.assertEqual(
            _find_default_progid_writes(nsh), [], "安装器不得写扩展名的默认 ProgID"
        )

    def test_默认ProgID检测正则可识别真实反例(self):
        """门禁正则必须对真实的坏写法成立，否则等于没有门禁。"""
        bad = [
            r'WriteRegStr SHELL_CONTEXT "Software\Classes\.png" "" "uvmp-toolkit.image"',
            r'WriteRegStr SHELL_CONTEXT "Software\Classes\.jpg" "" "app.jpg"',
            r'  WriteRegStr HKEY_CURRENT_USER "Software\Classes\.tiff" "" "x"',
            r'WriteRegStr SHELL_CONTEXT "Software\Classes\.${_EXT}" "" "${PROGID}"',
            r'WriteRegStr SHELL_CONTEXT "Software\Classes\.$R1" "" "app"',
        ]
        for line in bad:
            self.assertEqual(len(_find_default_progid_writes(line)), 1, line)

        good = [
            r'WriteRegStr SHELL_CONTEXT "Software\Classes\uvmp-toolkit.image" "" "图片"',
            r'WriteRegStr SHELL_CONTEXT "Software\Classes\uvmp-toolkit.image\DefaultIcon" "" "$INSTDIR\a.exe,0"',
            r'WriteRegStr SHELL_CONTEXT "Software\Classes\uvmp-toolkit.image\shell\open" "" "打开"',
            r'WriteRegNone SHELL_CONTEXT "Software\Classes\.png\OpenWithProgids" "uvmp-toolkit.image"',
            r'DeleteRegValue SHELL_CONTEXT "Software\Classes\.png" ""',
            r'ReadRegStr $R5 SHELL_CONTEXT "Software\Classes\.png" ""',
            r'; 不写 Software\Classes\.png 的默认值（注释里可以出现）',
        ]
        for line in good:
            self.assertEqual(_find_default_progid_writes(line), [], line)

    # ---- Linux 卸载：MIME 关联只在真正卸载时条件清理 ----

    def test_卸载MIME清理受dpkg动作门控(self):
        """dpkg 的 postrm 会以 `upgrade <旧版本>` 调用本脚本：此时新版本马上写回关联，
        清掉会让用户的默认项闪断甚至丢失，所以只在 remove / purge 时清。"""
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        body = _shell_function(
            _read(DESKTOP / "build" / "afterRemove.sh"), "uvmp_should_clean_mime", "afterRemove.sh"
        )
        cases = [
            ("remove", 0),
            ("purge", 0),
            ("disappear", 0),
            ("", 0),  # 手工执行（无参数）按卸载处理
            ("upgrade 2.1.0", 1),
            ("failed-upgrade 2.1.0", 1),
            ("abort-upgrade 2.1.0", 1),
            ("abort-remove", 1),
        ]
        for arg, expected in cases:
            with tempfile.TemporaryDirectory() as tmp:
                probe = Path(tmp) / "probe.sh"
                probe.write_text(
                    "set -u\nUVMP_POSTRM_ACTION=%s\n%s\nuvmp_should_clean_mime\n"
                    % (_sh_quote(arg), body),
                    encoding="utf-8",
                )
                out = subprocess.run([bash, str(probe)], capture_output=True, text=True)
                self.assertEqual(
                    out.returncode, expected, "动作 %r 判定错误：%s" % (arg, out.stderr)
                )

    def test_卸载MIME清理只在门控内执行(self):
        after = _strip_comments(_read(DESKTOP / "build" / "afterRemove.sh"), "#")
        self.assertIn("mimeapps.list", after)
        self.assertIn("uvmp-toolkit.desktop", after)
        self.assertNotIn("xdg-mime default", after, "卸载不能把默认值改指别家")
        m = re.search(r"^if uvmp_should_clean_mime; then\n(.*?)^fi$", after, re.S | re.M)
        self.assertIsNotNone(m, "MIME 清理必须包在 dpkg 动作门控里")
        block = m.group(1)
        self.assertIn("uvmp_strip_mimeapps", block, "门控里才允许调用清理")
        # 门控外不得再出现裸调用（否则 upgrade 照样会清）
        outside = after.replace(block, "")
        self.assertNotIn('uvmp_strip_mimeapps "$f"', outside, "门控外出现清理调用")

    def test_卸载MIME清理只删自己的条目(self):
        """用真 bash 跑一遍清理函数：别人的默认项、被共享的关联行都要保住。"""
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        script = _read(DESKTOP / "build" / "afterRemove.sh")
        body = _shell_function(script, "uvmp_strip_mimeapps", "afterRemove.sh")
        with tempfile.TemporaryDirectory() as tmp:
            conf = Path(tmp) / "mimeapps.list"
            conf.write_text(
                "[Default Applications]\n"
                "image/png=uvmp-toolkit.desktop\n"
                "image/jpeg=org.gnome.eog.desktop\n"
                "image/webp=org.gnome.eog.desktop;uvmp-toolkit.desktop;\n"
                "[Added Associations]\n"
                "image/gif=uvmp-toolkit.desktop;gimp.desktop;\n"
                "image/bmp=gimp.desktop;\n",
                encoding="utf-8",
            )
            probe = Path(tmp) / "probe.sh"
            probe.write_text(
                "set -eu\nDESKTOP_FILE=uvmp-toolkit.desktop\n%s\nuvmp_strip_mimeapps %s || true\n"
                % (body, _sh_quote(str(conf))),
                encoding="utf-8",
            )
            out = subprocess.run([bash, str(probe)], capture_output=True, text=True)
            self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
            text = conf.read_text(encoding="utf-8")
            self.assertNotIn("uvmp-toolkit.desktop", text, "自己的条目应被清掉：\n%s" % text)
            # 其他程序与用户原有默认项必须原样保留
            self.assertIn("image/jpeg=org.gnome.eog.desktop", text)
            self.assertIn("image/webp=org.gnome.eog.desktop;", text)
            self.assertIn("image/gif=gimp.desktop;", text)
            self.assertIn("image/bmp=gimp.desktop;", text)
            self.assertIn("[Default Applications]", text)
            self.assertIn("[Added Associations]", text)

    def test_卸载MIME清理只改含本工具的行(self):
        """无关行必须逐字保留：注释里的 `=`、不以分号结尾的值、多程序共享行都不能被
        重排/补分号——mimeapps.list 是用户的文件，清理只允许动自己那一段。"""
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        body = _shell_function(
            _read(DESKTOP / "build" / "afterRemove.sh"), "uvmp_strip_mimeapps", "afterRemove.sh"
        )
        source = (
            "# 说明：image/png=uvmp-toolkit.desktop 是本工具写入的（注释行，不许改）\n"
            "[Default Applications]\n"
            "image/png=uvmp-toolkit.desktop\n"
            "image/jpeg=org.gnome.eog.desktop\n"
            "image/webp=org.gnome.eog.desktop;uvmp-toolkit.desktop\n"
            "image/gif=gimp.desktop;uvmp-toolkit.desktop;krita.desktop\n"
            "[Added Associations]\n"
            "image/tiff=uvmp-toolkit.desktop;krita.desktop\n"
            "text/plain=gedit.desktop\n"
            "\n"
            "   \n"
        )
        expected = (
            "# 说明：image/png=uvmp-toolkit.desktop 是本工具写入的（注释行，不许改）\n"
            "[Default Applications]\n"
            "image/jpeg=org.gnome.eog.desktop\n"
            "image/webp=org.gnome.eog.desktop\n"
            "image/gif=gimp.desktop;krita.desktop\n"
            "[Added Associations]\n"
            "image/tiff=krita.desktop\n"
            "text/plain=gedit.desktop\n"
            "\n"
            "   \n"
        )
        with tempfile.TemporaryDirectory() as tmp:
            conf = Path(tmp) / "mimeapps.list"
            conf.write_text(source, encoding="utf-8")
            probe = Path(tmp) / "probe.sh"
            probe.write_text(
                "set -eu\nDESKTOP_FILE=uvmp-toolkit.desktop\n%s\nuvmp_strip_mimeapps %s || true\n"
                % (body, _sh_quote(str(conf))),
                encoding="utf-8",
            )
            out = subprocess.run([bash, str(probe)], capture_output=True, text=True)
            self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
            self.assertEqual(conf.read_text(encoding="utf-8"), expected)

    def test_卸载MIME清理不动无关文件(self):
        """没有本工具条目时文件一个字节都不改（不能把用户/其他程序的关联洗掉）。"""
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        body = _shell_function(
            _read(DESKTOP / "build" / "afterRemove.sh"), "uvmp_strip_mimeapps", "afterRemove.sh"
        )
        with tempfile.TemporaryDirectory() as tmp:
            conf = Path(tmp) / "mimeapps.list"
            original = (
                "# 用户手工维护\n[Default Applications]\nimage/png=org.gnome.eog.desktop\n"
                "[Added Associations]\nimage/jpeg=eog.desktop;\n"
            )
            conf.write_text(original, encoding="utf-8")
            probe = Path(tmp) / "probe.sh"
            probe.write_text(
                "set -eu\nDESKTOP_FILE=uvmp-toolkit.desktop\n%s\nuvmp_strip_mimeapps %s || true\n"
                % (body, _sh_quote(str(conf))),
                encoding="utf-8",
            )
            out = subprocess.run([bash, str(probe)], capture_output=True, text=True)
            self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
            self.assertEqual(conf.read_text(encoding="utf-8"), original)

    def test_卸载MIME清理跟随软链不改成普通文件(self):
        """mimeapps.list 可能是软链（用户把 ~/.config 整目录指向别处）。
        清理必须改真实文件，而不是用 mv 把软链替换成普通文件。"""
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        body = _shell_function(
            _read(DESKTOP / "build" / "afterRemove.sh"), "uvmp_strip_mimeapps", "afterRemove.sh"
        )
        # 1) 真软链：Windows 无 Developer Mode 时创建失败，跳过（Linux CI 会真跑）
        with tempfile.TemporaryDirectory() as tmp:
            real = Path(tmp) / "real" / "mimeapps.list"
            real.parent.mkdir()
            real.write_text(
                "[Default Applications]\nimage/png=uvmp-toolkit.desktop\n", encoding="utf-8"
            )
            link = Path(tmp) / "mimeapps.list"
            try:
                os.symlink(real, link)
            except (OSError, NotImplementedError) as exc:
                link = None
                self._assert_strip_follows_readlink(bash, body, tmp)
            if link is not None:
                probe = Path(tmp) / "probe.sh"
                probe.write_text(
                    "set -eu\nDESKTOP_FILE=uvmp-toolkit.desktop\n%s\nuvmp_strip_mimeapps %s || true\n"
                    % (body, _sh_quote(str(link))),
                    encoding="utf-8",
                )
                out = subprocess.run([bash, str(probe)], capture_output=True, text=True)
                self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
                self.assertTrue(os.path.islink(link), "软链被替换成了普通文件")
                self.assertNotIn("uvmp-toolkit.desktop", real.read_text(encoding="utf-8"))

    def _assert_strip_follows_readlink(self, bash, body, tmp):
        """没有真软链权限时的等价验证：注入 readlink，确认改的是解析后的真实路径。"""
        real = Path(tmp) / "resolved" / "mimeapps.list"
        real.parent.mkdir()
        real.write_text(
            "[Default Applications]\nimage/png=uvmp-toolkit.desktop\n", encoding="utf-8"
        )
        link = Path(tmp) / "link" / "mimeapps.list"
        link.parent.mkdir()
        link.write_text("[Default Applications]\nimage/png=uvmp-toolkit.desktop\n", encoding="utf-8")
        probe = Path(tmp) / "probe.sh"
        probe.write_text(
            "set -eu\nDESKTOP_FILE=uvmp-toolkit.desktop\n"
            # 模拟 readlink -f：把 link 路径解析成 resolved 路径
            "readlink() { printf '%%s\\n' %s ; }\n"
            "%s\nuvmp_strip_mimeapps %s || true\n"
            % (_sh_quote(str(real)), body, _sh_quote(str(link))),
            encoding="utf-8",
        )
        out = subprocess.run([bash, str(probe)], capture_output=True, text=True)
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertNotIn(
            "uvmp-toolkit.desktop",
            real.read_text(encoding="utf-8"),
            "清理应改 readlink 解析后的真实文件",
        )
        self.assertIn(
            "uvmp-toolkit.desktop",
            link.read_text(encoding="utf-8"),
            "未解析的链接路径不应被直接改写",
        )

    # ---- PyInstaller 收编断言：PIL 不能被注释满足，也不能躲在 playwright 分支里 ----

    def test_装配断言真的查PIL而非注释(self):
        """`assertIn("PIL", 函数体)` 会被解释性注释满足，等于没测。

        这里直接重放 `assert_core_modules`：给一份「有 apps 但没有 PIL」的归档清单必须失败，
        含 PIL 的清单必须通过。PIL 由独立的 with_imaging 参数控制（旧兼容分支 3.7 没有 Pillow），
        不能挂在 with_playwright 上——否则 Playwright 一旦可选，PIL 断言就被静默跳过。
        """
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        body = _shell_function(
            _read(PACKAGING / "build_on_device.sh"), "assert_core_modules", "build_on_device.sh"
        )
        with tempfile.TemporaryDirectory() as tmp:
            payload = _core_modules_probe(bash, tmp, body)
            base = "apps\nselftest\nwebsocket\npypdf\nCryptodome\n"
            full = base + "playwright\ngreenlet\nPIL\n"

            # 现代构建缺 PIL：必须失败（selfcheck 看不出来，只有真看图才炸）
            miss = payload(1, 1, base + "playwright\ngreenlet\n")
            self.assertNotEqual(miss.returncode, 0, "缺 PIL 竟然通过")
            self.assertIn("PIL", miss.stdout + miss.stderr)
            self.assertIn("漏收", miss.stdout + miss.stderr)

            # 有 PIL：通过，且清单里含 PIL
            ok = payload(1, 1, full)
            self.assertEqual(ok.returncode, 0, ok.stderr)
            self.assertEqual(
                ok.stdout.split(),
                ["apps", "selftest", "websocket", "pypdf", "Cryptodome", "playwright", "greenlet", "PIL"],
            )

            # 旧兼容分支（3.7，无 Pillow / Playwright）：不要求两者
            old = payload(0, 0, base)
            self.assertEqual(old.returncode, 0, old.stderr)
            self.assertNotIn("PIL", old.stdout.split())

            # with_playwright=0、with_imaging=1 时缺 PIL 仍必须失败：
            # 证明 PIL 断言不依赖 Playwright 开关
            miss2 = payload(0, 1, base)
            self.assertNotEqual(miss2.returncode, 0, "with_playwright=0 时 PIL 断言被跳过")

        # build_unix 始终是现代构建，PIL 直接进基础清单
        unix_body = _shell_function(
            _read(PACKAGING / "build_unix.sh"), "assert_core_bundle", "build_unix.sh"
        )
        mods_line = re.search(r'^ +mods="([^"]*)"', unix_body, re.M)
        self.assertIsNotNone(mods_line, "build_unix.sh 缺少归档模块清单")
        self.assertIn("PIL", mods_line.group(1).split(), "build_unix.sh 的基础归档清单必须含 PIL")

    def test_Windows装配断言真的查PIL(self):
        ps1 = _read(PACKAGING / "build_local.ps1")
        ps_body = _powershell_function(ps1, "Assert-CoreBundle")
        mods = re.search(r"\$mods = @\(([^)]*)\)", ps_body)
        self.assertIsNotNone(mods, "找不到 build_local.ps1 的归档模块清单")
        self.assertIn('"PIL"', mods.group(1), "build_local.ps1 的归档断言必须真的查 PIL")
        windows = _job_block(_read(REPO / ".github" / "workflows" / "release.yml"), "client-windows")
        m = re.search(r'foreach \(\$m in @\(([^)]*)\)\)', windows)
        self.assertIsNotNone(m, "CI Windows job 缺少归档模块断言")
        self.assertIn('"PIL"', m.group(1), "CI Windows job 的归档断言必须真的查 PIL")

    # ---- 离线安装：轮子必须与解释器 ABI 匹配，并真的探测 PIL ----

    def test_Linux安装脚本按解释器ABI筛轮(self):
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        body = _shell_function(
            _read(ROOT / "install" / "linux" / "install.sh"),
            "uvmp_wheel_fits_python",
            "install.sh",
        )
        cases = [
            ("cp315", "pillow-13.0.0-cp315-cp315-win_amd64.whl", 0),
            ("cp37", "pillow-13.0.0-cp315-cp315-win_amd64.whl", 1),
            ("cp310", "pillow-13.0.0-cp315-cp315-manylinux_2_27_x86_64.manylinux_2_28_x86_64.whl", 1),
            ("cp315", "pycryptodomex-3.24.2-cp37-abi3-manylinux2014_x86_64.whl", 0),
            ("cp37", "pycryptodomex-3.24.2-cp37-abi3-manylinux2014_x86_64.whl", 0),
            ("cp37", "requests-2.31.0-py3-none-any.whl", 0),
            ("cp315", "typing_extensions-4.7.1-py3-none-any.whl", 0),
        ]
        with tempfile.TemporaryDirectory() as tmp:
            for tag, wheel, expected in cases:
                probe = Path(tmp) / "probe.sh"
                probe.write_text(
                    "set -u\nPY_VER_TAG=%s\n%s\nuvmp_wheel_fits_python %s\n"
                    % (_sh_quote(tag), body, _sh_quote(wheel)),
                    encoding="utf-8",
                )
                out = subprocess.run([bash, str(probe)], capture_output=True, text=True)
                self.assertEqual(
                    out.returncode, expected, "%s / %s 判定错误：%s" % (tag, wheel, out.stderr)
                )

    def test_Windows安装脚本按解释器ABI筛轮(self):
        """没有 Windows 可跑，这里把 install.bat 的 findstr 规则原样重放一遍：

        - 纯 Python 轮（none-any）与 abi3 轮（pycryptodomex cp37-abi3）永远解；
        - 其余 `-cp3` / `-cp4` 二进制轮只在解释器标签（如 cp315）命中时解。
        """
        bat = _read(ROOT / "install" / "windows" / "install.bat")
        self.assertIn("PY_VER_TAG", bat, "install.bat 必须按解释器版本筛轮")
        self.assertIn("abi3", bat, "install.bat 必须放行 abi3 轮")
        self.assertIn("none-any", bat, "install.bat 必须放行纯 Python 轮")
        self.assertIn("PIL", bat, "install.bat 的依赖探测必须包含 PIL")

        def decide(name, tag):
            low = name.lower()
            if "abi3" in low or "none-any" in low:
                return True
            if "-cp3" in low or "-cp4" in low:
                return tag.lower() in low
            return True

        wheels = [p.name for p in (ROOT / "offline_packages").glob("*.whl")]
        self.assertTrue(wheels, "offline_packages 为空")
        pillow = [w for w in wheels if w.lower().startswith("pillow-")]
        self.assertTrue(pillow)
        for name in pillow:
            self.assertTrue(decide(name, "cp315"), "3.15 解释器应解 Pillow 轮：%s" % name)
            self.assertFalse(decide(name, "cp37"), "3.7 解释器不该解 Pillow 轮：%s" % name)
        for name in wheels:
            if "abi3" in name or "none-any" in name:
                self.assertTrue(decide(name, "cp37"), "纯 Python / abi3 轮必须放行：%s" % name)

    # ---- 构建脚本不裸装 Pillow（版本唯一来源是 requirements-core.txt）----

    def test_构建脚本不裸装Pillow(self):
        for name in self.SCRIPTS + ("build_linux_container.sh",):
            text = _read(PACKAGING / name)
            for m in re.finditer("pip install[^\\n]*pillow[^\\n]*", text, re.I):
                self.assertIn("==", m.group(0), "%s 裸装 Pillow：%s" % (name, m.group(0)))


def _bat_marked_block(text, marker, name):
    """抽出 .bat 里 `rem ::<marker>::` 与 `rem ::/<marker>::` 之间的可执行片段。

    测试要跑的是「真实可执行的那几行」，注释与说明不该参与，也不该被当成实现。
    """
    start_token = "rem ::%s::" % marker
    end_token = "rem ::/%s::" % marker
    lines = text.splitlines()
    try:
        start = next(i for i, ln in enumerate(lines) if start_token in ln)
        end = next(i for i, ln in enumerate(lines) if end_token in ln and i > start)
    except StopIteration:
        raise AssertionError("%s 缺少标记 %s" % (name, start_token)) from None
    block = [ln for ln in lines[start + 1 : end] if ln.strip()]
    # 测试里用 LF 拼接，交给调用方按需转 CRLF
    return "\n".join(block) + "\n"


def _find_default_progid_writes(text):
    """返回会改写 `Software\\Classes\\.<ext>` 默认 ProgID 的 WriteRegStr 行（行号, 内容）。

    NSIS 源码里反斜杠只有一个（`Software\\Classes\\.png` 在脚本里写作单反斜杠），
    正则也必须只匹配一个——写成 `\\\\\\\\` 会要求两个反斜杠，门禁永远不触发。
    """
    pattern = re.compile(
        r'WriteRegStr\s+\S+\s+"Software\\Classes\\\.'
        r'(?:\$\{?[A-Za-z_][A-Za-z0-9_]*\}?|[A-Za-z0-9._$]+)"\s+""'
    )
    hits = []
    for i, line in enumerate(text.splitlines(), 1):
        if pattern.search(line):
            hits.append((i, line.strip()))
    return hits


class PythonVersionBoundaryTest(unittest.TestCase):
    """构建分界是 3.11（Pillow 13 要求）：面向开发者的文档不能停留在 3.10。"""

    DOCS = (
        (REPO / "AGENTS.md", "AGENTS.md"),
        (REPO / "开发指南.md", "开发指南.md"),
        (ROOT / "README.md", "uvmp-toolkit/README.md"),
    )

    def test_文档构建分界写3_11(self):
        for path, label in self.DOCS:
            text = _read(path)
            self.assertIn("3.11", text, "%s 未提到 3.11 构建分界" % label)
            self.assertNotRegex(text, r"以 Python 3\.10 为分界", label)
            self.assertNotRegex(text, r"低于 3\.10 的解释器上只产出旧版兼容核心", label)

    def test_本机构建脚本注释不再声称3_12是最低依赖(self):
        text = _read(PACKAGING / "build_local.ps1")
        self.assertNotRegex(
            text,
            r"3\.12\+（requirements-core\.txt 的最低要求）",
            "requirements-core.txt 的最低要求是 3.11（Pillow 13），不能写成 3.12",
        )
        self.assertNotRegex(text, r"必须是 3\.12\+", "注释里的最低版本不能写成 3.12")

    def test_Unix构建脚本不再声称3_12是最低依赖(self):
        """build_unix.sh 里残留过「完整构建请用 Python 3.12+」，与 3.11 分界冲突。"""
        text = _read(PACKAGING / "build_unix.sh")
        self.assertNotRegex(
            text,
            r"Python 3\.12\+",
            "build_unix.sh 仍声称最低 3.12，与 requirements-core.txt（Pillow 13 → 3.11+）不一致",
        )
        self.assertIn("3.11", text, "build_unix.sh 应写明 3.11 构建分界")


class RuntimeLazyImportBundleTest(unittest.TestCase):
    """运行时才 import 的模块必须显式 --hidden-import，否则冻结版只在用户点开功能时才炸。

    Task 2 报告暴露的形态：
    - `toolkit/rpc.py` 的图片 RPC 是**方法内**惰性 `import imaging.engine / imaging.service`；
    - `toolkit/apps/batch_rename/backend.py` 的 `from fileops import naming`，而这个 backend
      本身由注册器按**文件路径** importlib 加载，PyInstaller 的静态分析根本看不到该文件。
    只 `--collect-all PIL` 只能收编 Pillow 包数据，收不到这两个自研模块。

    **清单一处维护**：模块名与「前置依赖」都取自 `toolkit/app.py` 的 `SELFCHECK_IMPORTS`
    （AST 读字面量），构建脚本、selfcheck、门禁三方共用同一份口径，避免三处各写一份漂移。
    """

    # 文件 → PyInstaller 命令的续行符（bash 用反斜杠，PowerShell / YAML 用反引号）
    ENTRIES = (
        (PACKAGING / "build_unix.sh", "\\"),
        (PACKAGING / "build_on_device.sh", "\\"),
        (PACKAGING / "build_local.ps1", "`"),
        (REPO / ".github" / "workflows" / "release.yml", "`"),
    )
    # 这四处都只跑现代构建（内置 CPython 3.15.2），图片模块无条件收编
    ALWAYS_MODERN = (
        PACKAGING / "build_unix.sh",
        PACKAGING / "build_local.ps1",
        REPO / ".github" / "workflows" / "release.yml",
    )
    # build_on_device.sh 还要服务 Python < 3.11 的旧兼容分支：那一支没有 Pillow
    DEVICE = PACKAGING / "build_on_device.sh"

    def setUp(self):
        self.manifest = _selfcheck_manifest()
        self.gated = tuple(m for m, gate in self.manifest if gate)
        self.always = tuple(m for m, gate in self.manifest if not gate)
        self.assertTrue(self.gated, "清单里应当有受前置依赖约束的模块（imaging.*）")
        self.assertTrue(self.always, "清单里应当有无条件必需的模块（fileops.naming）")

    def test_清单里的模块在源码中真实存在(self):
        for module in self.gated + self.always:
            top = module.split(".")[0]
            self.assertTrue((ROOT / "toolkit" / top).exists(), "源码缺少模块目录 %s" % top)
        rpc = _read(ROOT / "toolkit" / "rpc.py")
        for module in self.gated:
            self.assertRegex(
                rpc,
                r"(?m)^[ \t]+import %s" % re.escape(module),
                "rpc.py 中 %s 应为方法内惰性 import" % module,
            )

    def test_现代构建入口无条件收编全部清单项(self):
        for path in self.ALWAYS_MODERN:
            found = _hidden_imports(_pyinstaller_command(_read(path), self._cont(path)))
            for module in self.gated + self.always:
                self.assertIn(module, found, "%s 缺少 --hidden-import %s" % (path.name, module))

    def test_旧兼容分支不拼接受门控的hidden_import(self):
        """build_on_device.sh 的 <3.11 分支没有 Pillow：imaging.* 必须按开关拼接。

        这里直接重放脚本里的 `uvmp_init_imaging_args`，看 WITH_IMAGING=0/1 时的数组内容——
        不是读注释、不是正则猜，而是真跑一遍 shell 拼接逻辑。
        """
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        body = _shell_function(
            _read(self.DEVICE), "uvmp_init_imaging_args", self.DEVICE.name
        )
        with tempfile.TemporaryDirectory() as tmp:
            for flag, expect in (("0", []), ("1", list(self.gated))):
                probe = Path(tmp) / "probe.sh"
                probe.write_text(
                    "set -u\nWITH_IMAGING=%s\n%s\nuvmp_init_imaging_args\n"
                    'for a in "${IMAGING_ARGS[@]}"; do printf "%%s\\n" "$a"; done\n'
                    % (flag, body),
                    encoding="utf-8",
                )
                out = subprocess.run([bash, str(probe)], capture_output=True, text=True)
                self.assertEqual(out.returncode, 0, out.stderr)
                # 数组里应只有 --hidden-import 与其后的模块名成对出现
                modules = [
                    tok for tok in out.stdout.split()
                    if tok != "--hidden-import"
                ]
                self.assertEqual(
                    sorted(modules), sorted(expect), "WITH_IMAGING=%s 时的模块集合不对" % flag
                )
                self.assertEqual(
                    out.stdout.count("--hidden-import"),
                    len(expect),
                    "WITH_IMAGING=%s 时参数个数不对" % flag,
                )

    def test_build_on_device命令用数组拼接而非硬编码imaging(self):
        block = _pyinstaller_command(_read(self.DEVICE), "\\")
        self.assertIn("${IMAGING_ARGS[@]}", block, "imaging 模块必须走数组拼接")
        for module in self.gated:
            self.assertNotRegex(
                block,
                r"--hidden-import\s+%s" % re.escape(module),
                "命令里不得硬编码 %s（旧分支会因此收编依赖 Pillow 的模块）" % module,
            )
        # fileops.naming 不依赖 Pillow，任何分支都要收编
        for module in self.always:
            self.assertIn("--hidden-import %s" % module, block)

    def test_注释不能冒充hidden_import(self):
        """把真实声明改成注释后必须被判为缺失——否则门禁会被解释性注释满足。"""
        for path, cont in self.ENTRIES:
            # build_on_device.sh 的 imaging.* 走 IMAGING_ARGS 数组拼接（旧分支不收编），
            # 字面量只检查无条件必需项；数组内容由 test_旧兼容分支不拼接受门控的hidden_import 覆盖。
            modules = self.always if path == self.DEVICE else self.gated + self.always
            block = _pyinstaller_command(_read(path), cont)
            for module in modules:
                mutated = block.replace(
                    "--hidden-import %s" % module, "# --hidden-import %s" % module
                )
                self.assertNotEqual(mutated, block, "%s 未出现 %s" % (path.name, module))
                self.assertNotIn(
                    module,
                    _hidden_imports(mutated),
                    "%s 的注释写法被当成了真实声明（%s）" % (path.name, module),
                )

    def test_缺少任一模块都会被精确检出(self):
        """逐条移除：每次只少一个，检查器必须点名缺的是哪一个。"""
        for path in self.ALWAYS_MODERN:
            block = _pyinstaller_command(_read(path), self._cont(path))
            for module in self.gated + self.always:
                mutated = block.replace(" --hidden-import %s" % module, "")
                self.assertNotEqual(mutated, block, "%s 未出现 %s" % (path.name, module))
                found = _hidden_imports(mutated)
                missing = [m for m in self.gated + self.always if m not in found]
                self.assertEqual(
                    missing, [module], "%s 移除 %s 后检出的缺失项不对" % (path.name, module)
                )

    def test_命令块确实是PyInstaller入口而非别处(self):
        for path, cont in self.ENTRIES:
            block = _pyinstaller_command(_read(path), cont)
            self.assertIn("PyInstaller", block, path.name)
            self.assertRegex(block, r"toolkit[/\\]app\.py\s*$", "%s 命令块结尾不是 app.py" % path.name)

    @staticmethod
    def _cont(path):
        return "`" if path.suffix in (".ps1", ".yml") else "\\"


class SelfcheckImportSmokeTest(unittest.TestCase):
    """selfcheck 是冻结包唯一的构建期运行门禁：它必须真的 import 这些惰性模块。

    `scan_apps()` 会把 backend 加载失败只当作「后端缺失」打印，且 rpc 的图片导入是
    方法内惰性——所以「selfcheck 通过」此前并不覆盖 imaging/fileops。这里把它补上，
    并证明「缺模块时确实失败」而不是只靠字符串断言。

    同时要兼容旧兼容核心（Python < 3.11、不含 Pillow）：缺 PIL 时 imaging.* 整组跳过并
    明确报告「图片功能未启用」，**不算失败**；`fileops.naming` 与 Pillow 无关，仍然必需。
    """

    def setUp(self):
        self.app_py = ROOT / "toolkit" / "app.py"
        self.manifest = _selfcheck_manifest()
        self.gated = tuple(m for m, gate in self.manifest if gate)
        self.always = tuple(m for m, gate in self.manifest if not gate)

    def test_selfcheck导入清单含imaging与fileops(self):
        declared = [m for m, _ in self.manifest]
        for module in self.gated + self.always:
            self.assertIn(module, declared, "selfcheck 未覆盖 %s" % module)
        # 受门控的模块必须声明前置依赖，否则旧兼容核心会失败
        for module, gate in self.manifest:
            if module in self.gated:
                self.assertTrue(gate, "%s 应声明前置依赖" % module)

    def test_导入清单用字面量元组而非注释(self):
        """用 ast 取字面量：注释掉一行后清单必须真的变短（注释不能冒充）。"""
        import ast

        text = _read(self.app_py)
        line = next(
            ln for ln in text.splitlines() if '"imaging.engine"' in ln and not ln.strip().startswith("#")
        )
        mutated = text.replace(line, "# " + line.strip(), 1)
        self.assertNotEqual(mutated, text)
        values = []
        for node in ast.parse(mutated).body:
            if isinstance(node, ast.Assign) and any(
                isinstance(t, ast.Name) and t.id == "SELFCHECK_IMPORTS" for t in node.targets
            ):
                values.append(ast.literal_eval(node.value))
        self.assertTrue(values, "变异后找不到 SELFCHECK_IMPORTS")
        self.assertNotIn(
            "imaging.engine", [m for m, _ in values[0]], "注释行被当成了真实导入"
        )

    def test_真实selfcheck输出包含清单各项(self):
        py = _python_for_selfcheck()
        if py is None:
            self.skipTest("本机没有可用的 python 解释器")
        out = subprocess.run(
            [py, "toolkit/app.py", "selfcheck"],
            cwd=str(ROOT),
            capture_output=True,
            text=True,
            timeout=300,
        )
        output = out.stdout + out.stderr
        self.assertEqual(out.returncode, 0, output[-2000:])
        for module in self.gated + self.always:
            self.assertIn("导入 OK: %s" % module, output, output[-2000:])
        self.assertNotIn("导入失败", output, output[-2000:])

    def test_缺模块时selfcheck明确失败(self):
        """真实反例：让必需模块 import 失败，check 必须报失败且点名模块。"""
        py = _python_for_selfcheck()
        if py is None:
            self.skipTest("本机没有可用的 python 解释器")
        target = self.always[0]
        snippet = (
            "import sys\n"
            "sys.path.insert(0, 'toolkit')\n"
            "import app\n"
            "real = __import__\n"
            "def broken(name, *a, **k):\n"
            "    if name == %r:\n"
            "        raise ModuleNotFoundError(\"No module named %%r\" %% name)\n"
            "    return real(name, *a, **k)\n"
            "failures = app.check_runtime_imports(importer=broken, reporter=print)\n"
            "print('FAILURES=%%s' %% [n for n, _ in failures])\n"
            "sys.exit(0 if failures else 3)\n" % target
        )
        out = subprocess.run(
            [py, "-c", snippet], cwd=str(ROOT), capture_output=True, text=True, timeout=300
        )
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertIn("FAILURES=[%r]" % target, out.stdout)
        self.assertIn("导入失败: %s" % target, out.stdout)

    def test_旧兼容核心缺PIL时跳过图片模块并报告未启用(self):
        """Python < 3.11 的核心不含 Pillow：imaging.* 不能判失败，必须明确报告未启用。"""
        py = _python_for_selfcheck()
        if py is None:
            self.skipTest("本机没有可用的 python 解释器")
        snippet = (
            "import sys\n"
            "sys.path.insert(0, 'toolkit')\n"
            "import app\n"
            "real = __import__\n"
            "def old_core(name, *a, **k):\n"
            "    if name == 'PIL':\n"
            "        raise ModuleNotFoundError(\"No module named 'PIL'\")\n"
            "    return real(name, *a, **k)\n"
            "failures = app.check_runtime_imports(importer=old_core, reporter=print)\n"
            "print('FAILURES=%s' % [n for n, _ in failures])\n"
            "sys.exit(0 if not failures else 3)\n"
        )
        out = subprocess.run(
            [py, "-c", snippet], cwd=str(ROOT), capture_output=True, text=True, timeout=300
        )
        output = out.stdout + out.stderr
        self.assertEqual(out.returncode, 0, output[-2000:])
        self.assertIn("FAILURES=[]", out.stdout, output[-2000:])
        self.assertIn("图片功能未启用", output, output[-2000:])
        for module in self.gated:
            self.assertIn(module, output, "未启用报告里应点名 %s" % module)
        self.assertNotIn("导入失败", output, output[-2000:])
        # fileops.naming 与 Pillow 无关，仍必须被导入（输出里有 OK）
        for module in self.always:
            self.assertIn("导入 OK: %s" % module, output, output[-2000:])

    def test_旧兼容核心里fileops失败仍然报错(self):
        """跳过 imaging 不能顺带把 fileops 也放过：那会让批量重命名静默缺模块。"""
        py = _python_for_selfcheck()
        if py is None:
            self.skipTest("本机没有可用的 python 解释器")
        target = self.always[0]
        snippet = (
            "import sys\n"
            "sys.path.insert(0, 'toolkit')\n"
            "import app\n"
            "real = __import__\n"
            "def broken(name, *a, **k):\n"
            "    if name in ('PIL', %r):\n"
            "        raise ModuleNotFoundError('No module named %%s' %% name)\n"
            "    return real(name, *a, **k)\n"
            "failures = app.check_runtime_imports(importer=broken, reporter=print)\n"
            "print('FAILURES=%%s' %% [n for n, _ in failures])\n"
            "sys.exit(0 if failures else 3)\n" % target
        )
        out = subprocess.run(
            [py, "-c", snippet], cwd=str(ROOT), capture_output=True, text=True, timeout=300
        )
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertIn("FAILURES=[%r]" % target, out.stdout)
        self.assertIn("图片功能未启用", out.stdout, "缺 PIL 仍应报告未启用")

    def test_导入冒烟无副作用(self):
        """selfcheck 不能因为这次补的 import 去建状态目录/写文件。"""
        py = _python_for_selfcheck()
        if py is None:
            self.skipTest("本机没有可用的 python 解释器")
        with tempfile.TemporaryDirectory() as tmp:
            env = dict(os.environ)
            # 只改「状态目录」真正读的变量：state.state_dir() 在 Windows 用 LOCALAPPDATA、
            # 其它平台用 XDG_DATA_HOME。不能顺手改 APPDATA/HOME/USERPROFILE——
            # Windows 的 user site-packages（Pillow 就装在那里）靠它们定位，改了会误判成缺依赖。
            env["XDG_DATA_HOME"] = tmp
            env["LOCALAPPDATA"] = tmp
            env["UVMP_TOOLKIT_CONFIG"] = str(Path(tmp) / "config.json")
            snippet = (
                "import sys\n"
                "sys.path.insert(0, 'toolkit')\n"
                "import app\n"
                "failures = app.check_runtime_imports()\n"
                "assert not failures, failures\n"
            )
            out = subprocess.run(
                [py, "-c", snippet],
                cwd=str(ROOT),
                capture_output=True,
                text=True,
                timeout=300,
                env=env,
            )
            self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
            self.assertEqual(
                sorted(p.name for p in Path(tmp).iterdir()), [], "导入冒烟产生了副作用文件/目录"
            )


class InstallDependencyFallbackTest(unittest.TestCase):
    """源码安装脚本（免 pip、解 wheel 到 pylib）的两级依赖口径。

    背景：`offline_packages/` 里的 Pillow 只有 `cp315` 轮。装在 3.11–3.14 的机器上时，
    没有任何一轮 ABI 匹配 → 图片功能确实不可用，但**核心依赖（同步/导出/登录）齐全**，
    安装必须继续，并且明确警告；不能因为 Pillow 缺失整装失败（旧行为 `exit 1`）。
    旧解释器（<3.11）本来就不探测图片依赖，只提示不启用。

    另一个坑：模块清单变量（`UVMP_DEP_*` / `DEP_*`）如果只赋值不参与探测，就是死变量——
    看起来「有版本适配」，实际探测的还是写死的旧清单。这里逐条验证清单真的被用于探测。
    """

    LINUX = ROOT / "install" / "linux" / "install.sh"
    WINDOWS = ROOT / "install" / "windows" / "install.bat"

    def setUp(self):
        self.text = _read(self.LINUX)
        self.bash = shutil.which("bash")

    def _linux_probe(self, tmp, tag, missing, wheels):
        """组装一个可重放的 install.sh 依赖流程：假解释器 + 临时 project 目录。"""
        project = Path(tmp) / "proj"
        (project / "offline_packages").mkdir(parents=True)
        for name in wheels:
            # 只造文件名，解包交给假解释器的 -m zipfile 空操作
            (project / "offline_packages" / name).write_bytes(b"")
        fake = Path(tmp) / "fake-python"
        # 假解释器要认三种调用形态（与 install.sh 的 uvmp_init_python_compat /
        # uvmp_import_ok 实际写法一致）：
        #   $1=-c  $2=代码      → 版本探测
        #   $1=-m  zipfile ...  → 解包空操作
        #   $1=-   $2=pylib $3=模块清单 → 读 stdin 的探测脚本，按 FAKE_MISSING 决定成败
        fake.write_text(
            "#!/bin/bash\n"
            'if [ "$1" = "-c" ]; then\n'
            '  case "$2" in\n'
            "    *'cp%d%d'*) printf '%s\\n' \"$FAKE_TAG\" ;;\n"
            "    *'version_info[0]'*) printf '%s\\n' \"$FAKE_MAJOR\" ;;\n"
            "    *'version_info[1]'*) printf '%s\\n' \"$FAKE_MINOR\" ;;\n"
            "    *) exit 0 ;;\n"
            "  esac\n"
            "  exit 0\n"
            "fi\n"
            'if [ "$1" = "-m" ]; then exit 0; fi\n'
            'if [ "$1" = "-" ]; then\n'
            '  cat >/dev/null\n'
            '  mods="$3"\n'
            '  [ -n "$FAKE_MISSING" ] || exit 0\n'
            '  case "$mods" in *"$FAKE_MISSING"*) exit 1 ;; esac\n'
            "  exit 0\n"
            "fi\n"
            "exit 0\n",
            encoding="utf-8",
        )
        subprocess.run([self.bash, "-c", 'chmod +x "$1"', "bash", str(fake)], check=True)
        major, minor = tag[2], tag[3:]
        env = dict(os.environ)
        env.update(
            {
                "FAKE_TAG": tag,
                "FAKE_MAJOR": major,
                "FAKE_MINOR": minor,
                "FAKE_MISSING": missing,
            }
        )
        funcs = "\n".join(
            _shell_function(self.text, name, "install.sh")
            for name in (
                "uvmp_init_python_compat",
                "uvmp_wheel_fits_python",
                "uvmp_import_ok",
                "uvmp_report_imaging_unavailable",
                "uvmp_prepare_deps",
            )
        )
        probe = Path(tmp) / "probe.sh"
        probe.write_text(
            # 与 install.sh 的真实调用点一致：核心依赖失败时由调用方打提示并退出
            "set -u\nPYTHON=%s\nPROJECT_DIR=%s\nARCH=x86_64\n%s\n"
            "uvmp_init_python_compat\n"
            "uvmp_prepare_deps || { echo \"      依赖校验失败，请检查 offline_packages/\"; exit 1; }\n"
            % (_sh_quote(str(fake)), _sh_quote(str(project)), funcs),
            encoding="utf-8",
        )
        return subprocess.run(
            [self.bash, str(probe)], capture_output=True, text=True, env=env
        ), project

    def test_311到314只有cp315轮时只警告不阻断(self):
        """这正是审查指出的场景：3.14 机器 + 只有 cp315 的 Pillow。"""
        if not self.bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        for tag in ("cp311", "cp312", "cp313", "cp314"):
            with tempfile.TemporaryDirectory() as tmp:
                out, project = self._linux_probe(
                    tmp,
                    tag,
                    missing="PIL",
                    wheels=[
                        "requests-2.34.2-py3-none-any.whl",
                        "pillow-13.0.0-cp315-cp315-manylinux_2_27_x86_64.manylinux_2_28_x86_64.whl",
                    ],
                )
                text = out.stdout + out.stderr
                self.assertEqual(out.returncode, 0, "%s 竟然整装失败：%s" % (tag, text[-1500:]))
                self.assertIn("警告", text, "%s 缺 Pillow 必须明确警告" % tag)
                self.assertIn("图片", text, "%s 的警告要点明图片功能" % tag)
                self.assertNotIn("依赖校验失败", text, "%s 不应把图片缺失当成核心依赖失败" % tag)
                # ABI 不匹配的 cp315 轮不该被解包
                self.assertFalse(
                    (project / "pylib" / "PIL").exists(), "%s 不该解 cp315 的 Pillow 轮" % tag
                )

    def test_315解释器装了PIL就不警告(self):
        if not self.bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        with tempfile.TemporaryDirectory() as tmp:
            out, _ = self._linux_probe(
                tmp,
                "cp315",
                missing="@@none@@",
                wheels=["pillow-13.0.0-cp315-cp315-manylinux_2_27_x86_64.whl"],
            )
            text = out.stdout + out.stderr
            self.assertEqual(out.returncode, 0, text[-1500:])
            self.assertNotIn("警告", text, "Pillow 可用时不该警告")
            self.assertIn("图片依赖 OK", text)

    def test_核心依赖缺失仍然中止(self):
        """图片是可选，核心不是：gmssl/Cryptodome 缺失必须中止安装。"""
        if not self.bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        with tempfile.TemporaryDirectory() as tmp:
            out, _ = self._linux_probe(
                tmp,
                "cp315",
                missing="requests",
                wheels=["requests-2.34.2-py3-none-any.whl"],
            )
            text = out.stdout + out.stderr
            self.assertNotEqual(out.returncode, 0, "核心依赖缺失必须中止：" + text[-1500:])
            self.assertIn("依赖校验失败", text)

    def test_旧解释器只提示不启用图片(self):
        if not self.bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        with tempfile.TemporaryDirectory() as tmp:
            out, _ = self._linux_probe(
                tmp,
                "cp37",
                missing="@@none@@",
                wheels=["requests-2.31.0-py3-none-any.whl"],
            )
            text = out.stdout + out.stderr
            self.assertEqual(out.returncode, 0, text[-1500:])
            self.assertIn("不启用", text, "旧解释器应明确说明图片工具不启用")

    def test_Linux模块清单变量都真的参与探测(self):
        body = _shell_function(self.text, "uvmp_prepare_deps", "install.sh")
        names = set(re.findall(r"(UVMP_DEP_[A-Z_]+)=", self.text))
        self.assertIn("UVMP_DEP_CORE", names)
        self.assertIn("UVMP_DEP_IMAGING", names)
        for name in names:
            self.assertIn(
                '"$%s"' % name,
                body,
                "%s 只赋值没用于探测（死变量）" % name,
            )
        # 探测必须真的把清单交给解释器，而不是重新写死一份 import 列表
        self.assertIn("uvmp_import_ok", body)
        import_ok = _shell_function(self.text, "uvmp_import_ok", "install.sh")
        self.assertIn("sys.argv[2]", import_ok, "探测函数必须从参数读取模块清单")
        self.assertIn("__import__", import_ok)

    def test_Windows缺PIL只警告不阻断且清单参与探测(self):
        bat = _read(self.WINDOWS)
        names = set(re.findall(r'set "(DEP_[A-Z_]+)=([^"]*)"', bat))
        assigned = {name for name, _ in names} | {
            m.group(1) for m in re.finditer(r'set "(DEP_[A-Z_]+)=([^"]*)"', bat)
        }
        self.assertIn("DEP_CORE", assigned)
        self.assertIn("DEP_IMAGING", assigned)
        for name in assigned:
            # 每个清单变量都必须出现在某条 python 探测命令里
            probes = [
                ln
                for ln in bat.splitlines()
                if "__import__" in ln and "%%%s%%" % name in ln
            ]
            self.assertTrue(probes, "%s 只赋值没用于探测（死变量）" % name)

        # 图片探测失败只能警告，不能 exit /b 1
        lines = bat.splitlines()
        imaging_probe = next(
            i for i, ln in enumerate(lines)
            if "__import__" in ln and "%DEP_IMAGING%" in ln
        )
        tail = "\n".join(lines[imaging_probe : imaging_probe + 8])
        self.assertIn("警告", tail, "缺 Pillow 必须明确警告")
        self.assertNotRegex(
            tail, r"exit /b [1-9]", "缺 Pillow 不得中止安装（图片是可选能力）"
        )
        # 核心探测失败仍然中止
        core_probe = next(
            i for i, ln in enumerate(lines) if "__import__" in ln and "%DEP_CORE%" in ln
        )
        core_tail = "\n".join(lines[core_probe : core_probe + 30])
        self.assertRegex(core_tail, r"exit /b 1", "核心依赖缺失必须中止安装")

    # ---- 复审：解包触发条件与 Windows 版本读取的真实可执行性 ----

    def test_核心已齐只缺Pillow时仍会解包匹配轮(self):
        """升级场景：pylib 里核心依赖早装好了，只缺 Pillow。

        解包条件若只写「核心依赖缺失」，这时就直接跳过解包，然后报「没有匹配轮」
        ——可离线轮明明就在包里，用户永远补不上图片功能。
        """
        if not self.bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        with tempfile.TemporaryDirectory() as tmp:
            out, project = self._linux_probe(
                tmp,
                "cp315",
                missing="PIL",
                wheels=[
                    "requests-2.34.2-py3-none-any.whl",
                    "pillow-13.0.0-cp315-cp315-manylinux_2_27_x86_64.manylinux_2_28_x86_64.whl",
                ],
            )
            text = out.stdout + out.stderr
            self.assertEqual(out.returncode, 0, text[-1500:])
            marker = project / "pylib" / ".unpacked"
            self.assertTrue(
                marker.exists(),
                "核心已齐、只缺 Pillow 时必须解包（否则离线轮永远装不上）：\n%s" % text[-1500:],
            )
            unpacked = marker.read_text(encoding="utf-8").split()
            self.assertTrue(
                any("pillow-13.0.0-cp315" in name for name in unpacked),
                "应解包与解释器匹配的 Pillow 轮：%s" % unpacked,
            )
            # 解包后仍缺（假解释器永远 import 失败）：只警告，不阻断
            self.assertIn("警告", text)
            self.assertNotIn("依赖校验失败", text)

    def test_依赖齐全时不重复解包(self):
        if not self.bash:
            self.skipTest("无 bash，无法重放 shell 断言")
        with tempfile.TemporaryDirectory() as tmp:
            out, project = self._linux_probe(
                tmp,
                "cp315",
                missing="@@none@@",
                wheels=["pillow-13.0.0-cp315-cp315-manylinux_2_27_x86_64.whl"],
            )
            text = out.stdout + out.stderr
            self.assertEqual(out.returncode, 0, text[-1500:])
            self.assertFalse(
                (project / "pylib" / ".unpacked").exists(), "依赖齐全时不应解包"
            )
            self.assertIn("图片依赖 OK", text)

    def test_图片提示不再谎称轮子不存在(self):
        """解包过之后仍装不上时，提示要说「已尝试解包但当前解释器用不上」，
        不能笼统说 offline_packages 里没有该轮（它确实在）。"""
        with tempfile.TemporaryDirectory() as tmp:
            out, project = self._linux_probe(
                tmp,
                "cp311",
                missing="PIL",
                wheels=["pillow-13.0.0-cp315-cp315-manylinux_2_27_x86_64.whl"],
            )
            text = out.stdout + out.stderr
            self.assertEqual(out.returncode, 0, text[-1500:])
            self.assertIn("没有与 cp311 匹配", text)
            # cp311 用不上 cp315 轮 → 不该把它解进 pylib
            marker = project / "pylib" / ".unpacked"
            unpacked = marker.read_text(encoding="utf-8") if marker.exists() else ""
            self.assertNotIn("pillow-13.0.0-cp315", unpacked)

    def test_Windows版本读取在真实cmd下输出cp315(self):
        """`for /f` 捕获带引号的解释器路径会失败（cmd 会剥外层引号、括号还需转义）。

        这里把 install.bat 里真正的探测片段抽出来，交给真实 cmd.exe 跑：
        含空格与不含空格的 Python 路径都必须打印出 `cp315`。
        """
        cmd = shutil.which("cmd")
        if cmd is None:
            self.skipTest("非 Windows 环境，无法真实运行 cmd.exe")
        bat = _read(self.WINDOWS)
        block = _bat_marked_block(bat, "UVMP_PYVER_PROBE", "install.bat")
        # 批处理里 %% 是转义：源码写 %%d%%d，cmd 折叠后交给 Python 的是 %d%d
        self.assertIn("%%d%%d", block, "源码里应写 %%d%%d（cmd 折叠成 %d%d）")

        real_py = sys.executable
        if not real_py.lower().endswith("python.exe"):
            self.skipTest("当前解释器不是 python.exe：%s" % real_py)

        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            # 造一个含空格的路径：用 junction 指向真实 Python 目录（无需管理员）
            py_dir = Path(real_py).parent
            spaced_dir = tmp_path / "py dir"
            spaced_dir.mkdir()
            junction = spaced_dir / "pycore"
            made = subprocess.run(
                ["cmd", "/c", "mklink", "/J", str(junction), str(py_dir)],
                capture_output=True,
                text=True,
                errors="replace",  # mklink 输出是系统本地代码页（中文 Windows 为 GBK）
            )
            spaced_py = junction / "python.exe"
            candidates = [(real_py, "无空格")]
            if made.returncode == 0 and spaced_py.exists():
                candidates.append((str(spaced_py), "含空格"))

            for py_path, label in candidates:
                script = tmp_path / ("probe_%d.bat" % len(label))
                script.write_text(
                    "@echo off\r\n"
                    "setlocal EnableExtensions\r\n"
                    "chcp 65001 >nul\r\n"
                    'set "PYTHONW=%s"\r\n' % py_path
                    + block.replace("\r\n", "\n").replace("\n", "\r\n")
                    + "\r\necho RESULT=[%PY_VER_TAG%]\r\nendlocal\r\n",
                    encoding="utf-8",
                    newline="",
                )
                run = subprocess.run(
                    [cmd, "/c", str(script)], capture_output=True, text=True, errors="replace"
                )
                out = (run.stdout + run.stderr).replace("\r", "")
                self.assertIn(
                    "RESULT=[cp315]",
                    out,
                    "%s 路径下版本读取失败（%s）：\n%s" % (label, py_path, out[-1500:]),
                )

    def test_文档说明selfcheck旧核心跳过而非失败(self):
        """文档必须讲清「旧兼容缺 PIL 时跳过并提示未启用」，避免被当成导入失败去乱改构建。"""
        docs = (
            (ROOT / "内网工具箱开发指南.md", "内网工具箱开发指南.md"),
            (REPO / "开发指南.md", "开发指南.md"),
        )
        for path, label in docs:
            text = _read(path)
            self.assertIn("图片功能未启用", text, "%s 未说明降级提示" % label)
            self.assertRegex(
                text,
                r"跳过[^\n]{0,40}(不算失败|不是失败)|(不算失败|不是失败)[^\n]{0,20}跳过",
                "%s 未说明「跳过而非失败」" % label,
            )

    def test_Windows版本读取失败有明确提示(self):
        bat = _read(self.WINDOWS)
        block = _bat_marked_block(bat, "UVMP_PYVER_PROBE", "install.bat")
        self.assertNotRegex(
            block, r"for\s+/f", "不得再用 for /f 捕获解释器输出（引号与括号会破坏解析）"
        )
        self.assertIn("set /p PY_VER_TAG=", block, "应改用临时文件 + set /p 读取")
        # 失败时必须有可读提示
        self.assertRegex(
            bat, r"if not defined PY_VER_TAG \(\s*\r?\n\s*echo \[错误\]", "缺少版本读取失败提示"
        )

    def test_过期注释已更新(self):
        """「selfcheck 照样过、只有打开图片才炸」在补了导入冒烟后已不成立。"""
        for path in (
            PACKAGING / "build_unix.sh",
            PACKAGING / "build_on_device.sh",
            PACKAGING / "build_local.ps1",
            REPO / ".github" / "workflows" / "release.yml",
        ):
            text = _read(path)
            self.assertNotIn("selfcheck 照样过", text, "%s 仍有过期注释" % path.name)
            self.assertNotRegex(
                text, r"只有真打开图片才炸", "%s 仍有过期注释" % path.name
            )


def _python_for_selfcheck():
    """selfcheck 需要能 import requests 等依赖；优先当前解释器，其次 PATH 上的 python。"""
    candidates = [sys.executable]
    for name in ("python", "python3"):
        found = shutil.which(name)
        if found:
            candidates.append(found)
    for exe in candidates:
        if not exe:
            continue
        probe = subprocess.run(
            [exe, "-c", "import requests, pypdf"], capture_output=True, text=True
        )
        if probe.returncode == 0:
            return exe
    return None


def _pyinstaller_command(text, continuation):
    """取出文件里真正的 PyInstaller 命令行（含续行），不含任何注释行。

    只认「同一处 `-m PyInstaller` 起、按续行符连到 `toolkit/app.py` 结束」的块，
    避免把注释里提到的模块名当成真实参数。
    """
    lines = text.splitlines()
    for i, line in enumerate(lines):
        code = line.split("#", 1)[0] if not line.lstrip().startswith("#") else ""
        # 必须精确匹配 `-m PyInstaller` 后接空白：`-m PyInstaller.utils.cliutils...`
        # 是读归档清单的辅助命令，不是构建入口。
        if not re.search(r"-m\s+PyInstaller\s", code):
            continue
        block = [line]
        j = i
        while block[-1].rstrip().endswith(continuation):
            j += 1
            if j >= len(lines):
                raise AssertionError("PyInstaller 命令续行到文件结尾仍未结束")
            block.append(lines[j])
        return "\n".join(block)
    raise AssertionError("找不到 PyInstaller 命令")


def _hidden_imports(block):
    """从命令块里抽 --hidden-import 目标；注释行/行尾注释一律先剔除。"""
    code_lines = []
    for line in block.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        code_lines.append(line.split("#", 1)[0])
    return re.findall(r"--hidden-import\s+([A-Za-z_][\w.]*)", "\n".join(code_lines))


def _selfcheck_manifest(app_py=None):
    """从 toolkit/app.py 的 SELFCHECK_IMPORTS 读「(模块, 前置依赖)」清单（AST 字面量）。

    这是全仓唯一口径：构建脚本的 --hidden-import、selfcheck 的导入冒烟、这里的门禁
    都对齐它，避免出现第二份手写清单各自漂移。
    """
    import ast

    tree = ast.parse(_read(app_py or (ROOT / "toolkit" / "app.py")))
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(
            isinstance(t, ast.Name) and t.id == "SELFCHECK_IMPORTS" for t in node.targets
        ):
            value = ast.literal_eval(node.value)
            manifest = tuple((str(m), gate) for m, gate in value)
            if not manifest:
                raise AssertionError("SELFCHECK_IMPORTS 为空")
            return manifest
    raise AssertionError("app.py 缺少 SELFCHECK_IMPORTS")


if __name__ == "__main__":
    unittest.main()
