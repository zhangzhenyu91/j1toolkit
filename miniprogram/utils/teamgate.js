// 班组切换器 + 生效班组门控（原在 pkg-filetransfer / pkg-quiz index / pkg-safeday / pkg-quiz pool·manage
// 近乎逐行重复，抽此复用）。createTeamGate(options) → Behavior，页面 behaviors 引入：
//   storageKey  超管选中班组的本地持久化 key（如 filetransfer_team_id）
//   withAll     true 时切换器含「全部班组」选项（safeday；生效值 this._teamSel 为 'all' 或班组 id，
//               并在 data 增加 showTeamPill——仅「全部班组」混排视图在记录行显示班组徽章）；
//               false 时生效值 this._teamId（0=不带参数，后端落自己/默认班组）
//   teamNameKey 非空时把生效班组名额外存入该 storage key（quiz 的 quiz_team_name，manage 页「上传至」展示用）
// 页面侧职责：
//   passGate 内调 this.passTeamGate(user, onReady)——非超管未分配班组 → noTeam 整页空态（返回 false，
//   不再发业务请求）；非超管直接 onReady()；超管拉班组定生效班组（storage 优先 → 自己班组 → 第一项）后 onReady()
//   实现 onTeamSwitched()：超管主动切换班组后重拉本页数据（applyTeam 后回调）
// 仅读取生效班组的二级页（pool/manage）不调 passTeamGate，自行从 storage 恢复 this._teamId 后用 teamQuery/teamBody
const { request } = require('./request');

function createTeamGate({ storageKey, withAll = false, teamNameKey = '' }) {
  return Behavior({
    data: {
      // 班组切换器：超管可点 chip 下拉切换；其余角色为静态班组名标签
      isAdmin: false,
      noTeam: false, // 非超管且未分配班组：整页空态，不发业务请求
      teamName: '',
      teamOptions: [], // [{id(withAll 时含 'all'), name, on}]
      teamDropOpen: false,
      ...(withAll ? { showTeamPill: false } : {}),
    },

    methods: {
      // 门控：user 为当前用户；onReady 为确定生效班组后的首屏加载回调。返回 false=noTeam 空态
      passTeamGate(user, onReady) {
        this._role = user.role || 'user';
        // 生效班组选中值（仅超管经切换器改变；非超管保持初始值，后端强制本班）
        if (withAll) this._teamSel = 'all';
        else this._teamId = 0;
        // 非超管且未分配班组：整页空态，不再发任何业务请求
        if (this._role !== 'admin' && !user.team) {
          this.setData({ gate: true, noTeam: true, loading: false });
          return false;
        }
        this.setData({ gate: true, isAdmin: this._role === 'admin', teamName: user.team || '' });
        if (this._role === 'admin') {
          this.initTeams(user, onReady); // 超管先定生效班组，再加载数据
          return true;
        }
        if (onReady) onReady();
        return true;
      },

      // 超管：拉启用班组（/admin/teams 取 status=1）→ 生效班组（storage 优先 → 自己班组 → 第一个）→ onReady
      async initTeams(user, onReady) {
        let teams = [];
        try {
          const data = await request({ url: '/api/v1/admin/teams' });
          teams = ((data && data.list) || []).filter((t) => t.status === 1);
        } catch (err) {
          this.toast(err.message);
        }
        this._teams = teams;
        let sel;
        if (withAll) {
          const saved = String(wx.getStorageSync(storageKey) || '');
          sel = 'all';
          if (saved === 'all' || (saved && teams.some((t) => t.id === Number(saved)))) {
            sel = saved === 'all' ? 'all' : Number(saved);
          } else if (teams.some((t) => t.id === Number(user.team_id))) {
            sel = Number(user.team_id);
          }
        } else {
          const saved = Number(wx.getStorageSync(storageKey)) || 0;
          const cur = teams.find((t) => t.id === saved)
            || teams.find((t) => t.id === Number(user.team_id))
            || teams[0] || null;
          sel = cur ? cur.id : 0;
        }
        this.applyTeam(sel, false);
        if (onReady) onReady();
      },

      // 记录当前生效班组并刷新切换器展示；switching=true 表示用户主动切换，回调 onTeamSwitched 重拉数据
      applyTeam(sel, switching) {
        if (withAll) {
          this._teamSel = sel;
          wx.setStorageSync(storageKey, String(sel));
          const all = sel === 'all';
          const cur = all ? null : (this._teams || []).find((t) => t.id === sel);
          this.setData({
            teamName: all ? '全部班组' : (cur ? cur.name : this.data.teamName),
            teamDropOpen: false,
            showTeamPill: all, // 仅「全部班组」混排视图显示班组徽章
            teamOptions: [{ id: 'all', name: '全部班组', on: all }].concat(
              (this._teams || []).map((t) => ({ id: t.id, name: t.name, on: !all && t.id === sel }))
            ),
          });
        } else {
          this._teamId = sel;
          if (sel) wx.setStorageSync(storageKey, sel);
          const cur = ((this._teams || []).find((t) => t.id === sel)) || null;
          // 生效班组名一并存下（manage 页「上传至」选项展示用）
          if (teamNameKey && cur) wx.setStorageSync(teamNameKey, cur.name);
          this.setData({
            teamName: cur ? cur.name : this.data.teamName,
            teamDropOpen: false,
            teamOptions: (this._teams || []).map((t) => ({ id: t.id, name: t.name, on: t.id === sel })),
          });
        }
        if (switching && this.onTeamSwitched) this.onTeamSwitched();
      },

      // 生效班组 query 片段（lead 为前导连接符；仅超管生效班组确定后携带，其余角色后端强制本班无需传；
      // withAll 模式固定 '?team_id=all|id' 口径）
      teamQuery(lead) {
        if (withAll) return this._role === 'admin' ? `?team_id=${this._teamSel || 'all'}` : '';
        return this._teamId ? `${lead || '&'}team_id=${this._teamId}` : '';
      },

      // 生效班组 body 注入（POST JSON 用，口径同 teamQuery）
      teamBody(data) {
        return this._teamId ? Object.assign({}, data, { team_id: this._teamId }) : data;
      },

      onTeamChipTap() {
        if (!this.data.isAdmin || !(this._teams || []).length) return;
        this.setData({ teamDropOpen: !this.data.teamDropOpen });
      },

      onTeamDropClose() {
        if (this.data.teamDropOpen) this.setData({ teamDropOpen: false });
      },

      onTeamPick(e) {
        const raw = e.currentTarget.dataset.id;
        const sel = withAll && raw === 'all' ? 'all' : Number(raw);
        if (!sel || sel === (withAll ? this._teamSel : this._teamId)) {
          this.setData({ teamDropOpen: false });
          return;
        }
        this.applyTeam(sel, true);
      },
    },
  });
}

module.exports = { createTeamGate };
