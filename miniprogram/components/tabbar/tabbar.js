// 底部标签栏组件
Component({
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
