import { reactive } from 'vue';

// 执行历史「参数回填」单向事件：历史页写入，业务页在 activated/监听时消费。
export const restoreState = reactive({
  appID: '',
  params: null,
  nonce: 0,
});

export function requestRestore(appID, params) {
  restoreState.appID = appID;
  restoreState.params = params;
  restoreState.nonce += 1;
}
