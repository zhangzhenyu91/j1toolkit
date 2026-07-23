// 底部标签栏组件
Component({
  properties: {
    active: { type: String, value: 'home' }, // 当前页：home / me
  },

  methods: {
    go(e) {
      const { url, key } = e.currentTarget.dataset;
      if (key === this.data.active) return; // 已在当前页
      wx.reLaunch({ url });
    },
  },
});
