// 杆塔三级级联弹层共享（电压等级 → 线路名称 → 杆塔号；原在 pkg-wmadd/pages/index 与
// pkg-worklog/pages/index 逐字重复，抽此复用）。createTowerCascade(options) → Behavior，页面 behaviors 引入：
//   towersUrl(page)  杆塔坐标接口 URL（出工日志需拼生效班组 teamQuery；wmadd 不带参数，后端按登录人班组取数）
//   cacheKey(page)   坐标缓存 storage key（按班组隔离——wmadd=本人班组 id / 出工日志=生效班组 id，避免串班组的旧缓存）
//   logTag           console.error 日志标签（如 水印添加）
// 页面侧职责：
//   提供 toast(message)；data 自备 keyboardHeight 与 onKeyboardHeight（出工日志多弹层共用，故不入本 Behavior）；
//   「选择杆塔坐标」入口与「确定」回调因各包分流不同留在页面：
//     onOpenTower() 内调 this.openTowerCascade()；onTowerConfirm() 默认分支调 this.applyTowerToWm(t)
//   依赖页面同时引入 utils/wmphoto.js 的 createWmPhoto（applyTowerToWm 用 jitterCoord/refreshWmGeo，
//   wmTowerPicked 亦供其 fillWmByLocation 判「已选杆塔不再覆盖」）
const { request } = require('./request');

function createTowerCascade({ towersUrl, cacheKey, logTag }) {
  return Behavior({
    data: {
      wmTowerPicked: null, // 已选杆塔 { level, line, no, lng, lat }；未选为 null（已选后天气/地点行显「已更新」标）
      towerVisible: false,
      towerLoading: false,
      towerRows: null, // 全量行 [电压等级, 线路名称, 杆塔号, 经度, 纬度]（storage 缓存优先，后台静默刷新）
      towerOpen: '', // 当前展开选项列表的级：level / line / tower（''=全收起）
      towerLevels: [],
      towerLevel: '',
      towerLines: [], // 当前展示的线路选项（= 本电压等级全部线路按 towerLineKw 关键字过滤）
      towerLineKw: '', // 线路名称输入框内容（搜索关键字 / 已选线路名）
      towerLine: '',
      towerTowers: [], // 当前展示的杆塔号选项（= 本线路全部杆塔按 towerTowerKw 关键字过滤）[{ no, lng, lat, lngText, latText }]
      towerTower: null,
      towerTowerKw: '', // 杆塔号输入框内容（搜索关键字 / 已选杆塔号）
      towerScrollInto: '', // 聚焦输入框所在行 id（键盘弹起时滚动区 scroll-into-view 到该行）
    },

    methods: {
      // 杆塔坐标数据：storage 缓存优先并后台静默刷新；无缓存则请求服务端（全量约 1366 行）
      loadTowerRows() {
        const KEY = cacheKey(this);
        const cached = wx.getStorageSync(KEY);
        if (cached && Array.isArray(cached.rows) && cached.rows.length) {
          request({ url: towersUrl(this), timeout: 10000 })
            .then((r) => { if (r && Array.isArray(r.rows) && r.rows.length) wx.setStorageSync(KEY, r); })
            .catch(() => {});
          return Promise.resolve(cached.rows);
        }
        return request({ url: towersUrl(this), timeout: 10000 }).then((r) => {
          if (!r || !Array.isArray(r.rows) || !r.rows.length) throw new Error('杆塔坐标数据为空');
          wx.setStorageSync(KEY, r);
          return r.rows;
        });
      },

      towerLevelsOf() {
        const rows = this.data.towerRows || [];
        return [...new Set(rows.map((r) => r[0]))];
      },

      towerLinesOf(level) {
        const rows = this.data.towerRows || [];
        return [...new Set(rows.filter((r) => r[0] === level).map((r) => r[1]))];
      },

      towerTowersOf(level, line) {
        const rows = this.data.towerRows || [];
        return rows
          .filter((r) => r[0] === level && r[1] === line)
          .map((r) => ({ no: r[2], lng: r[3], lat: r[4], lngText: r[3].toFixed(6), latText: r[4].toFixed(6) }));
      },

      // 每次进入水印编辑前重置杆塔选择态（towerRows 坐标数据缓存保留，供下次直接打开）
      resetTowerState() {
        this.setData({
          wmTowerPicked: null,
          towerVisible: false,
          towerOpen: '',
          towerLevel: '',
          towerLine: '',
          towerLineKw: '',
          towerLevels: this.towerLevelsOf(),
          towerLines: [],
          towerTowers: [],
          towerTower: null,
          towerTowerKw: '',
          towerScrollInto: '',
          keyboardHeight: 0,
        });
      },

      // 打开级联弹层（页面「选择杆塔坐标」入口包装调用）；首次打开需先加载数据（失败关层提示，已选状态保留供重选带回）
      openTowerCascade() {
        wx.hideKeyboard(); // 收起施工内容等 hold-keyboard 输入残留的键盘，弹层统一从键盘收起态布局
        const base = { towerVisible: true, towerOpen: '', keyboardHeight: 0, towerScrollInto: '', towerLevels: this.towerLevelsOf() };
        if (this.data.towerRows) {
          this.setData(base);
          return;
        }
        this.setData({ ...base, towerLoading: true });
        this.loadTowerRows()
          .then((rows) => {
            if (!this.data.towerVisible) return;
            this.setData({
              towerRows: rows,
              towerLoading: false,
              towerLevels: [...new Set(rows.map((r) => r[0]))],
            });
          })
          .catch((err) => {
            console.error(`[${logTag}] 杆塔坐标加载失败：`, err);
            this.setData({ towerLoading: false, towerVisible: false });
            this.toast('杆塔坐标加载失败，请稍后重试');
          });
      },

      // 级联「确定」默认分支：所选杆塔坐标按 ≤50m 随机波动后填入水印表单（不直接带入原值，仍可手改），
      // 并再次调高德地图接口按波动后坐标覆盖刷新地点、天气
      applyTowerToWm(t) {
        const jittered = this.jitterCoord(t.lng, t.lat, 50);
        this.setData({
          towerVisible: false,
          wmTowerPicked: { level: this.data.towerLevel, line: this.data.towerLine, no: t.no, lng: t.lng, lat: t.lat },
          'wmForm.lng': jittered.lng,
          'wmForm.lat': jittered.lat,
        });
        this.refreshWmGeo(jittered.lng, jittered.lat, '已按杆塔坐标更新地点、天气');
      },

      // 展开/收起某级选项列表（禁用级不响应：选线路需先选电压等级，选杆塔需先选线路名称）；
      // 经箭头展开时恢复该级完整列表（清空输入过滤，便于改选）
      onTowerToggle(e) {
        const { key } = e.currentTarget.dataset;
        if (key === 'line' && !this.data.towerLevel) return;
        if (key === 'tower' && !this.data.towerLine) return;
        const open = this.data.towerOpen === key ? '' : key;
        const patch = { towerOpen: open };
        if (key === 'level' && open === 'level') patch.towerLevels = this.towerLevelsOf();
        if (key === 'line' && open === 'line') patch.towerLines = this.towerLinesOf(this.data.towerLevel);
        if (key === 'tower' && open === 'tower') patch.towerTowers = this.towerTowersOf(this.data.towerLevel, this.data.towerLine);
        this.setData(patch);
      },

      // 选中上级后清空下级并自动展开下一级选项
      onPickLevel(e) {
        wx.hideKeyboard(); // 选中即结束筛选输入，收起 hold-keyboard 残留键盘
        const v = e.currentTarget.dataset.v;
        if (v === this.data.towerLevel) {
          this.setData({ towerOpen: '' });
          return;
        }
        this.setData({
          towerLevel: v,
          towerLines: this.towerLinesOf(v),
          towerLine: '',
          towerLineKw: '',
          towerTowers: [],
          towerTower: null,
          towerTowerKw: '',
          towerOpen: 'line',
        });
      },

      // 线路名称输入：按关键字过滤下拉选项并展开；输入与已选值不一致时清空已选及下级
      onTowerLineInput(e) {
        const v = e.detail.value;
        const kw = v.trim();
        const all = this.towerLinesOf(this.data.towerLevel);
        const patch = {
          towerLineKw: v,
          towerLines: kw ? all.filter((n) => n.indexOf(kw) !== -1) : all,
          towerOpen: 'line',
        };
        if (this.data.towerLine && v !== this.data.towerLine) {
          patch.towerLine = '';
          patch.towerTower = null;
          patch.towerTowerKw = '';
          patch.towerTowers = [];
        }
        this.setData(patch);
      },

      onTowerLineFocus() {
        if (this.data.towerLevel) this.setData({ towerOpen: 'line', towerScrollInto: 'tower-row-line' });
      },

      onPickLine(e) {
        wx.hideKeyboard(); // 选中即结束筛选输入，收起 hold-keyboard 残留键盘
        const v = e.currentTarget.dataset.v;
        if (v === this.data.towerLine) {
          this.setData({ towerOpen: '' });
          return;
        }
        this.setData({
          towerLine: v,
          towerLineKw: v,
          towerTowers: this.towerTowersOf(this.data.towerLevel, v),
          towerTower: null,
          towerTowerKw: '',
          towerOpen: 'tower',
        });
      },

      // 杆塔号输入：按关键字过滤下拉选项并展开；输入与已选值不一致时清空已选
      onTowerTowerInput(e) {
        const v = e.detail.value;
        const kw = v.trim();
        const all = this.towerTowersOf(this.data.towerLevel, this.data.towerLine);
        const patch = {
          towerTowerKw: v,
          towerTowers: kw ? all.filter((t) => String(t.no).indexOf(kw) !== -1) : all,
          towerOpen: 'tower',
        };
        if (this.data.towerTower && v !== this.data.towerTower.no) patch.towerTower = null;
        this.setData(patch);
      },

      onTowerTowerFocus() {
        if (this.data.towerLine) this.setData({ towerOpen: 'tower', towerScrollInto: 'tower-row-tower' });
      },

      onPickTower(e) {
        wx.hideKeyboard(); // 选中即结束筛选输入，收起 hold-keyboard 残留键盘
        const t = this.data.towerTowers[e.currentTarget.dataset.i];
        if (!t) return;
        this.setData({ towerTower: t, towerTowerKw: t.no, towerOpen: '' });
      },

      // 级联输入框失焦：复位滚动定位，下次聚焦可再次触发 scroll-into-view
      onTowerInputBlur() {
        this.setData({ towerScrollInto: '' });
      },

      onTowerCancel() {
        wx.hideKeyboard(); // 关层前收起 hold-keyboard 残留键盘
        this.setData({ towerVisible: false });
      },

      onTowerVisibleChange(e) {
        if (!e.detail.visible) {
          wx.hideKeyboard(); // 遮罩关闭同步收起 hold-keyboard 残留键盘
          this.setData({ towerVisible: false });
        }
      },
    },
  });
}

module.exports = { createTowerCascade };
