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
import re
import shutil
import subprocess
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
                    self.assertEqual(
                        _run_shell_payload(bash, tmp, fn, root, rel),
                        str(root / rel),
                        "%s 未认 exe 同目录布局：%s" % (name, rel),
                    )
                # 布局 B（PyInstaller 6）：资产在 _internal/，根目录没有
                for rel in self.PAYLOAD:
                    (root / rel).unlink()
                    _touch(root / "_internal" / rel)
                for rel in self.PAYLOAD:
                    self.assertEqual(
                        _run_shell_payload(bash, tmp, fn, root, rel),
                        str(root / "_internal" / rel),
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
            ctx = {
                "appOutDir": str(out_dir),
                "electronPlatformName": "win32",
                "arch": 1,
                "packager": {"appInfo": {"version": "9.9.9"}},
            }
            ctx_json = json.dumps(ctx)
            script = Path(tmp) / "hook.cjs"
            script.write_text(
                "const hook = require(%s);\n"
                "hook.default(%s).then(() => console.log('OK'),"
                " (e) => { console.error(e.message); process.exit(3); });\n"
                % (json.dumps(str(DESKTOP / "build" / "prune-ort.cjs")), ctx_json),
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
  fileAssociations: [],
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


class BuildOnDeviceTest(unittest.TestCase):
    """离线构建脚本的「现代 / 旧兼容」分界必须与 requirements-core.txt 的最低 Python 一致。

    requirements 里的 requests 2.34 / pypdf 7 / pyinstaller 6 都要求 Python 3.10+：
    分界写成 3.9 会让 3.9 走现代路径、pip 找不到可用 wheel 后整锅失败。
    """

    def setUp(self):
        self.text = _read(PACKAGING / "build_on_device.sh")

    def test_分界为3_10(self):
        self.assertIn("sys.version_info < (3, 10)", self.text)
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


if __name__ == "__main__":
    unittest.main()
