import { createApp } from 'vue';
import App from './ImageToolsApp.vue';
import './imagetools.css';
import './animations.css';

// 独立图片窗口：不装 TDesign（窗口只由本目录组件与 token 构成），也不引入主题/CDN 资源。
createApp(App).mount('#app');
