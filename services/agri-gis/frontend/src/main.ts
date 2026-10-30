import { createPinia } from 'pinia'
import { createApp, h } from 'vue'
import 'ol/ol.css'

import App from './App.vue'
import router from './router/index'
import './styles/global.less'
import { legacyDemoEnabled } from './security/legacyIdentity'

const app = createApp(legacyDemoEnabled() ? App : {
  render: () => h('main', { role: 'status' }, [
    h('h1', '旧GIS工作台暂不可用'),
    h('p', '领域接口尚未接入可信平台授权。请使用农业平台入口；本页不会选择默认身份或提交旧业务请求。'),
    h('a', { href: '/agri/' }, '打开农业工作台'),
  ]),
})

app.use(createPinia())
if (legacyDemoEnabled()) app.use(router)
app.mount('#app')
