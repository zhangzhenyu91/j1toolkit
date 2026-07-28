// 出工日志：腾讯云 COS 封装（水印照片存取，独立前缀 COS_WORKLOG_PREFIX）
const COS = require('cos-nodejs-sdk-v5');
const config = require('../config');

let client = null;

function ensureConfigured() {
  const { secretId, secretKey, bucket, region } = config.cos;
  if (!secretId || !secretKey || !bucket || !region) {
    const err = new Error('腾讯云 COS 未配置（COS_SECRET_ID / COS_SECRET_KEY / COS_BUCKET / COS_REGION）');
    err.expose = true;
    throw err;
  }
}

function getClient() {
  ensureConfigured();
  if (!client) {
    client = new COS({ SecretId: config.cos.secretId, SecretKey: config.cos.secretKey });
  }
  return client;
}

// 上传 Buffer，返回 COS 对象键
function putBuffer(key, buf, contentType) {
  return new Promise((resolve, reject) => {
    getClient().putObject(
      {
        Bucket: config.cos.bucket,
        Region: config.cos.region,
        Key: key,
        Body: buf,
        ContentType: contentType || 'image/jpeg',
      },
      (err) => (err ? reject(err) : resolve(key))
    );
  });
}

function deleteObject(key) {
  return new Promise((resolve, reject) => {
    getClient().deleteObject(
      { Bucket: config.cos.bucket, Region: config.cos.region, Key: key },
      (err) => (err ? reject(err) : resolve())
    );
  });
}

// 访问地址：自定义域名优先（如 https://j1toolkit.cos.j1net.com），否则按标准域名拼接
function publicUrl(key) {
  const base = config.worklog.cosBaseUrl
    ? config.worklog.cosBaseUrl.replace(/\/+$/, '')
    : `https://${config.cos.bucket}.cos.${config.cos.region}.myqcloud.com`;
  return `${base}/${key}`;
}

module.exports = { putBuffer, deleteObject, publicUrl, ensureConfigured };
