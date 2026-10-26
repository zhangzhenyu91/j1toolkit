// 主应用页共用宽屏状态：isWide 用于布局/居中弹层，isTablet 用于固定尺寸与安全区。
function windowWidth() {
  try {
    const info = wx.getWindowInfo ? wx.getWindowInfo() : wx.getSystemInfoSync();
    return Number(info.windowWidth) || 0;
  } catch (e) {
    return 0;
  }
}

const initialWidth = windowWidth();

export default Behavior({
  data: {
    isWide: initialWidth >= 900,
    isTablet: initialWidth >= 600,
  },

  methods: {
    onResize() {
      const width = windowWidth();
      const isWide = width >= 900;
      const isTablet = width >= 600;
      if (isWide !== this.data.isWide || isTablet !== this.data.isTablet) {
        this.setData({ isWide, isTablet });
      }
      if (typeof this.resizeWmCrop === 'function') this.resizeWmCrop();
    },
  },
});
