# -*- coding: utf-8 -*-
"""旧 Python 导出流程的本地并发原语（不依赖 Electron / Go）。

- bounded_map：有界并行执行，结果按输入顺序收集；任务开始前检查取消
- RendererPool：渲染 worker 生命周期管理——每个槽位独占一个 UvmpClient 与一个旧
  OfficialTrackRenderer（各自 state 下独立目录、CDP 连接与浏览器 profile），按需懒启动，
  渲染异常后槽位可重建；只暴露一个 stop() 供任务取消钩子统一调用

测试可注入 fake client、renderer factory 与 job，不触碰真实网络与浏览器。
"""

import queue
import shutil
import threading
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path


def clamp_workers(value, default=1, low=1, high=8) -> int:
    """并发数钳制：非法/缺省回落 default，再夹到 [low, high]"""
    try:
        n = int(value)
    except (TypeError, ValueError):
        n = int(default)
    return max(int(low), min(int(high), n))


def bounded_map(items, fn, max_workers, job=None, on_result=None) -> list:
    """有界并行执行 fn(item)，最多 max_workers 个在途。

    - 结果与 items 同序；on_result(index, value) 在主线程按输入顺序回调，
      便于主线程有序更新进度/清单（worker 不碰共享状态）
    - job 非空时每个任务开始前 job.check_cancel()；取消抛出后取消未启动任务并原样上抛
    """
    items = list(items)
    if not items:
        return []
    workers = max(1, int(max_workers))
    results = []
    executor = ThreadPoolExecutor(max_workers=workers, thread_name_prefix="uvmp-par")
    futures = []
    try:
        for item in items:
            futures.append(executor.submit(_run_one, job, fn, item))
        for i, future in enumerate(futures):
            try:
                value = future.result()
            except BaseException:
                for f in futures:
                    f.cancel()
                raise
            results.append(value)
            if on_result is not None:
                on_result(i, value)
    finally:
        executor.shutdown(wait=True)
    return results


def _run_one(job, fn, item):
    if job is not None:
        job.check_cancel()
    return fn(item)


class RendererPoolClosed(RuntimeError):
    """渲染池已关闭（取消/收尾）后不再允许启动新浏览器"""


class RendererWorker:
    """单个渲染槽位：独占 client 与按需启动的渲染器，工作目录与其他槽位隔离。

    启动状态机（工厂可能很慢，取消不能被它阻塞）：
      - 工厂在锁外执行，持有锁的只可能是状态读写
      - close() 只标记关闭并关闭已存在的实例后立即返回；启动中的工厂由启动方
        返回后检查关闭标记，若已关闭则自行 stop，绝不泄漏浏览器
    """

    def __init__(self, index, work_dir, client, factory, log=print, closed=None):
        self.index = index
        self.work_dir = Path(work_dir)
        self.client = client
        self._factory = factory
        self._log = log
        self._closed_check = closed or (lambda: False)
        self._lock = threading.Lock()
        self._renderer = None
        self._started = False
        self._starting = False
        self._closed = False

    def _is_closed(self):
        return self._closed or bool(self._closed_check())

    def _stop_renderer(self, renderer):
        if renderer is None:
            return
        try:
            renderer.stop()
        except Exception as e:  # noqa: BLE001
            self._log("  [渲染] worker-%d 关闭异常：%s" % (self.index, e))

    @property
    def current_renderer(self):
        with self._lock:
            return self._renderer

    @property
    def renderer(self):
        """懒启动；factory 返回 None 表示渲染器不可用，后续调用不再反复启动"""
        with self._lock:
            if self._is_closed():
                raise RendererPoolClosed("渲染器池已关闭")
            if self._started:
                return self._renderer
            if self._starting:
                raise RuntimeError("渲染器正在启动")
            self._starting = True
        try:
            # 慢启动（浏览器/CDP 登录）不得持锁：取消钩子要能立刻返回
            renderer = self._factory(self.client, self.work_dir)
        except BaseException:
            with self._lock:
                self._starting = False
            raise
        with self._lock:
            self._starting = False
            closed = self._is_closed()
            if closed:
                self._started = False
                self._renderer = None
            else:
                self._renderer = renderer
                self._started = True
        if closed:
            # 启动期间被取消/收尾：由启动方自行关闭，避免取消后泄漏浏览器
            self._stop_renderer(renderer)
            raise RendererPoolClosed("渲染器池已关闭")
        return renderer

    def invalidate(self):
        """渲染失败：丢弃并关闭当前实例，下一项重建；不关闭槽位（目录保留）"""
        with self._lock:
            renderer, self._renderer = self._renderer, None
            self._started = False
        self._stop_renderer(renderer)

    def close(self):
        """取消/收尾：标记关闭并关闭已有实例；不等待启动中的工厂（启动方随后自停）"""
        with self._lock:
            self._closed = True
            renderer, self._renderer = self._renderer, None
            self._started = False
        self._stop_renderer(renderer)


class RendererPool:
    """渲染 worker 池：槽位固定、渲染器懒启动、取消时统一 stop"""

    def __init__(self, size, clients, factory, work_root, log=print):
        self.size = max(1, int(size))
        clients = list(clients)
        if len(clients) < self.size:
            raise ValueError(
                "renderer 客户端数不足：%d < %d" % (len(clients), self.size)
            )
        self.work_root = Path(work_root)
        self._log = log
        self._lock = threading.Lock()
        self._stopped = False
        self._workers = [
            RendererWorker(
                i,
                self.work_root / ("worker-%d" % i),
                clients[i],
                factory,
                log,
                closed=lambda: self._stopped,
            )
            for i in range(self.size)
        ]
        self._slots = queue.Queue()
        for i in range(self.size):
            self._slots.put(i)

    def worker(self, index) -> RendererWorker:
        return self._workers[index]

    def stage(self, items, handler, job, on_result=None) -> list:
        """有界并行执行 handler(item, worker)：每项独占一个渲染槽位"""
        items = list(items)
        if not items:
            return []

        def run(item):
            slot = self._slots.get()
            try:
                if job is not None:
                    job.check_cancel()
                return handler(item, self._workers[slot])
            finally:
                self._slots.put(slot)

        return bounded_map(items, run, self.size, job, on_result=on_result)

    def stop(self):
        """关闭全部渲染器；可作为任务取消钩子（幂等、不等待启动中的工厂）"""
        with self._lock:
            if self._stopped:
                return
            self._stopped = True
        for worker in self._workers:
            worker.close()

    def cleanup(self):
        """成功收尾：清理本 job 的临时 worker 目录（失败时调用方不调用，保留诊断）"""
        shutil.rmtree(str(self.work_root), ignore_errors=True)
