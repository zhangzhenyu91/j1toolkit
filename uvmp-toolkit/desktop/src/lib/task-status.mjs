import { reactive } from 'vue';

// 壳层运行中任务计数：runJob 起停时增减，页头显示；keep-alive 切页不影响。
export const taskStatus = reactive({ running: 0 });

export function addRunning(delta) {
  taskStatus.running = Math.max(0, taskStatus.running + delta);
}
