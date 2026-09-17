// 安全日活动记录：非 PDF 文件转 PDF（LibreOffice headless，供多文件合并前统一格式）
// soffice 为系统命令而非 npm 依赖：Docker 镜像须安装 LibreOffice（见 README/开发指南第八节）；
// 路径可用 SAFEDAY_SOFFICE_PATH 覆盖（默认 PATH 中的 soffice），未安装时转 ENOENT 友好报错
const fs = require('fs');
const os = require('os');
const path = require('path');
const { execFile } = require('child_process');
const config = require('../config');

const CONVERT_TIMEOUT = 120 * 1000; // 单文件转换超时 120s（大体积 ppt/xls 实测可能较慢）

// 单个非 PDF buffer → PDF buffer；ext 不含点（doc/docx/ppt/pptx/xls/xlsx）
// soffice 只能读写磁盘文件：mkdtemp 建临时目录，输入落盘 source.{ext}，转换后读回 source.pdf
function convertToPdf(buffer, ext) {
  return new Promise((resolve, reject) => {
    const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'safeday-cv-'));
    const cleanup = () => {
      try {
        fs.rmSync(tmp, { recursive: true, force: true });
      } catch (e) {
        /* ignore */
      }
    };
    const done = (err, buf) => {
      cleanup();
      if (err) reject(err);
      else resolve(buf);
    };
    try {
      const input = path.join(tmp, `source.${ext}`);
      fs.writeFileSync(input, buffer);
      // 每次转换独立 UserInstallation：避免并发/残留锁导致 headless 实例互斥失败
      execFile(
        config.safeday.sofficePath,
        [
          `-env:UserInstallation=file://${path.join(tmp, 'lo-profile')}`,
          '--headless',
          '--norestore',
          '--convert-to',
          'pdf',
          '--outdir',
          tmp,
          input,
        ],
        { timeout: CONVERT_TIMEOUT },
        (err, stdout, stderr) => {
          if (err) {
            if (err.code === 'ENOENT') {
              done(new Error('服务端未安装 LibreOffice（soffice），无法转换非 PDF 文件，请联系管理员'));
              return;
            }
            if (err.killed || err.signal === 'SIGTERM') {
              done(new Error('格式转换超时（120s），文件可能过大或损坏'));
              return;
            }
            done(new Error(`格式转换失败：${(stderr || err.message || '').slice(0, 200)}`));
            return;
          }
          const out = path.join(tmp, 'source.pdf');
          if (!fs.existsSync(out)) {
            done(new Error(`格式转换失败：未产出 PDF（${(stderr || stdout || '').slice(0, 200) || '无输出'}）`));
            return;
          }
          done(null, fs.readFileSync(out));
        }
      );
    } catch (e) {
      done(e);
    }
  });
}

module.exports = { convertToPdf };
