// 员工管理（users）/ 权限管理（perms）共用：启用班组加载 + 班组筛选 tab + 按班组分组的用户列表
// （两页该部分原近乎逐行重复，抽此 Behavior 复用）。页面差异经钩子收敛：
//   buildUsersQuery(query) —— 追加用户列表查询参数（users 页带 keyword）
//   mapUserItem(u) —— 用户项补充展示字段（users 页带 roleText）
const { request } = require('../../../utils/request');

module.exports = Behavior({
  data: {
    teams: [], // 启用班组（筛选 tab 数据源；users 页弹层班组下拉共用）
    users: [],
    groups: [], // 分组展示结构 [{ name, users }]
    teamTab: '', // 当前筛选：''=全部 0=未分组 其余=班组 id
  },

  methods: {
    async loadTeams() {
      try {
        const data = await request({ url: '/api/v1/admin/teams' });
        this.setData({ teams: (data.list || []).filter((t) => t.status === 1) });
        this.buildGroups(); // 分组依赖 teams+users 两个请求，后到的一方负责重建（此处 users 可能已就绪）
      } catch (err) {
        this.toast(err.message);
      }
    },

    // 用户列表额外查询参数（钩子，默认无）
    buildUsersQuery(query) {
      return query;
    },

    // 用户项补充展示字段（钩子，默认原样返回）
    mapUserItem(u) {
      return u;
    },

    async loadUsers() {
      this.setData({ loading: true });
      try {
        const query = this.buildUsersQuery(this.data.teamTab !== '' ? [`team_id=${this.data.teamTab}`] : []);
        const url = `/api/v1/admin/users${query.length ? `?${query.join('&')}` : ''}`;
        const data = await request({ url });
        const users = (data.list || []).map((u) => this.mapUserItem({
          ...u,
          char: (u.nickname || u.username || '?').slice(0, 1),
        }));
        this.setData({ users });
        this.buildGroups();
      } catch (err) {
        this.toast(err.message);
      } finally {
        this.setData({ loading: false });
      }
    },

    // 「全部」tab 按班组分组（未分组排最后）；指定 tab 仅一组
    buildGroups() {
      const { teamTab, teams, users } = this.data;
      let groups;
      if (teamTab === '') {
        groups = teams
          .map((t) => ({ name: t.name, users: users.filter((u) => u.team_id === t.id) }))
          .filter((g) => g.users.length > 0);
        const unassigned = users.filter((u) => !u.team_id);
        if (unassigned.length > 0) groups.push({ name: '未分组', users: unassigned });
      } else {
        const name = teamTab === 0 ? '未分组' : ((teams.find((t) => t.id === teamTab) || {}).name || '');
        groups = [{ name, users }];
      }
      this.setData({ groups });
    },

    // ---------- 筛选 ----------
    onTeamTab(e) {
      const raw = e.currentTarget.dataset.id;
      const teamTab = raw === '' ? '' : Number(raw);
      if (teamTab === this.data.teamTab) return;
      this.setData({ teamTab });
      this.loadUsers();
    },
  },
});
