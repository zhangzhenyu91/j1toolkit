#!/usr/bin/env python3
# GL KVM 文件分享 API（壹匣集成）
#
#   GET  /status               被动查询：共享状态 + 盘内文件列表（不切换共享状态）
#   POST /push                 multipart/form-data 上传（字段名 files，可多个）：
#                              共享中则先断开 → 文件写入 /userdata/media → sync；**结束保持非共享**，
#                              挂载由 /mount 单独触发（先传完所有文件再一次性挂载，避免逐文件弹跳）
#   POST /mount                挂载（共享）到被控机
#   GET  /list                 反向操作：确保断开共享（分区挂载回设备本机）→ 返回全部文件名
#   GET  /download/<文件名>    下载盘内文件（同样确保断开共享；URL 编码的文件名）
#
# 注意：/list 与 /download 结束后分区保持「非共享」状态（文件仅在本机挂载时可读），
#       被控机要重新看到 U 盘需再推送或到平台 UI 手动连接。
#
# 共享切换复用 kvmd 自带端点（经 unix socket /run/kvmd/kvmd.sock，路径不带 /api 前缀）：
#   GET /msd                          → result.drive_partition.connected（true=共享中）
#   GET /msd/partition_connect        → 共享给被控机（本地自动卸载）
#   GET /msd/partition_disconnect     → 断开共享（本地自动挂载回 /userdata/media）

import asyncio
import logging
import os
import re
from urllib.parse import quote

from aiohttp import ClientSession, UnixConnector, web

MEDIA_DIR = '/userdata/media'
KVMD_SOCK = '/run/kvmd/kvmd.sock'
LISTEN_HOST = '0.0.0.0'
LISTEN_PORT = 8901
SWITCH_TIMEOUT = 30  # 共享状态切换等待上限（秒）

log = logging.getLogger('fileshare')
media_lock = asyncio.Lock()  # 所有读写分区/切换共享的操作串行化，防止互相打断

# exfat 文件名非法字符
_ILLEGAL = re.compile(r'[\\/:*?"<>|\x00-\x1f]')


def safe_name(name):
    name = os.path.basename(name or '').strip().strip('.')
    return _ILLEGAL.sub('_', name) or 'unnamed'


async def kvmd_get(path):
    async with ClientSession(connector=UnixConnector(path=KVMD_SOCK)) as s:
        async with s.get('http://localhost' + path) as r:
            return await r.json()


async def msd_connected():
    data = await kvmd_get('/msd')
    return bool(data['result']['drive_partition']['connected'])


async def wait_state(target):
    # target=True 等到共享；False 等到非共享且分区已挂载回本地
    for _ in range(int(SWITCH_TIMEOUT * 2)):
        try:
            if await msd_connected() == target:
                if target and not os.path.ismount(MEDIA_DIR):
                    return
                if not target and os.path.ismount(MEDIA_DIR):
                    return
        except Exception:
            pass
        await asyncio.sleep(0.5)
    raise TimeoutError('等待共享状态切换超时')


async def set_shared(shared):
    await kvmd_get('/msd/partition_connect' if shared else '/msd/partition_disconnect')
    await wait_state(shared)


async def ensure_unshared():
    # 确保分区挂载在本机（共享中则先断开）；调用方须持有 media_lock
    if await msd_connected():
        await set_shared(False)


def list_files():
    out = []
    try:
        for name in sorted(os.listdir(MEDIA_DIR)):
            p = os.path.join(MEDIA_DIR, name)
            if os.path.isfile(p):
                out.append({'name': name, 'size': os.path.getsize(p)})
    except FileNotFoundError:
        pass
    return out


async def status(request):
    return web.json_response({
        'ok': True,
        'shared': await msd_connected(),
        'mounted': os.path.ismount(MEDIA_DIR),
        'files': list_files(),
    })


async def save_parts(request):
    saved = []
    reader = await request.multipart()
    while True:
        part = await reader.next()
        if part is None:
            break
        if not getattr(part, 'filename', None):
            continue
        name = safe_name(part.filename)
        path = os.path.join(MEDIA_DIR, name)
        size = 0
        with open(path, 'wb') as f:  # 同名覆盖
            while True:
                chunk = await part.read_chunk(256 * 1024)
                if not chunk:
                    break
                f.write(chunk)
                size += len(chunk)
        saved.append({'name': name, 'size': size})
        log.info('saved %s (%d bytes)', name, size)
    return saved


async def push(request):
    if not request.content_type.startswith('multipart/'):
        return web.json_response(
            {'ok': False, 'error': '需要 multipart/form-data（字段名 files）'}, status=400)

    async with media_lock:
        await ensure_unshared()

        try:
            saved = await save_parts(request)
        except Exception as e:
            log.exception('save failed')
            return web.json_response({'ok': False, 'error': '文件写入失败：%s' % e}, status=500)

        if not saved:
            return web.json_response({'ok': False, 'error': '未收到文件'}, status=400)

        os.sync()  # 落盘；保持非共享，挂载由 /mount 单独触发
        return web.json_response({'ok': True, 'saved': saved, 'shared': False})


async def mount(request):
    async with media_lock:
        await set_shared(True)
        return web.json_response({'ok': True, 'shared': True})


async def list_(request):
    async with media_lock:
        await ensure_unshared()
        return web.json_response({'ok': True, 'shared': False, 'files': list_files()})


async def download(request):
    name = safe_name(request.match_info['name'])
    async with media_lock:
        await ensure_unshared()
        path = os.path.join(MEDIA_DIR, name)
        if not os.path.isfile(path):
            return web.json_response({'ok': False, 'error': '文件不存在：%s' % name}, status=404)
        log.info('download %s (%d bytes)', name, os.path.getsize(path))
        # RFC 5987 编码中文文件名；下载期间持锁，防止推送切换共享把分区卸载
        return web.FileResponse(path, headers={
            'Content-Disposition': "attachment; filename*=UTF-8''%s" % quote(name),
        })


def main():
    logging.basicConfig(level=logging.INFO, format='%(name)s: %(message)s')
    app = web.Application(client_max_size=0)  # 上传大小不限（局域网可信来源；盘 26.8G 自有限制）
    app.router.add_get('/status', status)
    app.router.add_post('/push', push)
    app.router.add_post('/mount', mount)
    app.router.add_get('/list', list_)
    app.router.add_get('/download/{name}', download)
    web.run_app(app, host=LISTEN_HOST, port=LISTEN_PORT, print=None)


if __name__ == '__main__':
    main()
