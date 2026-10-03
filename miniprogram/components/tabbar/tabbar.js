// 底部标签栏组件
Component({
  // apply-shared：让 app.wxss 全局样式（.hover-scale 按压反馈）渗入组件，默认隔离下 hover-class 静默失效
  options: { styleIsolation: 'apply-shared' },

  properties: {
    active: { type: String, value: 'home' }, // 当前面板：home / me
  },

  methods: {
    go(e) {
      const { key } = e.currentTarget.dataset;
      if (key === this.data.active) return; // 已在当前面板
      this.triggerEvent('switch', { key }); // 由页面切换面板（左右滑动动画）
    },
  },
});
