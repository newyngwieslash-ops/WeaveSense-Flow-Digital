<script setup lang="ts">
import { onMounted, onUnmounted, shallowRef } from 'vue'
import { SessionController, loginURL, workspaceURL } from './session'
import { bindSessionLifecycle } from './session-lifecycle'
const controller = new SessionController()
const state = shallowRef(controller.state)
controller.onChange = (value) => { state.value = value }
let signIn = '/login?next=%2Fagri%2F'
try { signIn = loginURL(window.location.search) } catch { /* Invalid context is reported by refresh. */ }
const refresh = () => { void controller.refresh(window.location.search) }
let unbind = () => {}
onMounted(() => { unbind = bindSessionLifecycle(controller, window, document, () => document.visibilityState, () => window.location.search) })
onUnmounted(() => { unbind() })
</script>

<template>
  <main :data-session-status="state.status">
    <header><a class="brand" href="/">WeaveSense · DigitalTwin</a><nav aria-label="产品导航"><a href="/">平台工作区</a><a href="/agri/">农业工作台</a></nav></header>
    <p class="eyebrow">农业数字孪生 / 统一账号接入</p>
    <h1>农业工作台</h1>
    <p>与平台共享登录会话；不再使用本地用户编号切换身份。</p>
    <p v-if="state.status === 'loading' || state.status === 'logging_out'" role="status">{{ state.status === 'logging_out' ? '正在验证退出结果…' : '正在验证平台会话和工作区…' }}</p>
    <section v-if="state.error" role="alert"><h2>暂时无法进入</h2><p>{{ state.error.message }}</p><code>{{ state.error.code }}</code></section>
    <section v-if="state.status === 'ready' && state.user">
      <h2>当前平台账号</h2><p data-testid="session-user" :data-user-id="state.user.id">{{ state.user.displayName }} · {{ state.user.username }}</p>
      <p class="muted">身份来自服务器会话。显示账号不代表已获得农业资源访问权限。</p>
      <button type="button" data-testid="session-logout" @click="controller.logout()">退出统一账号</button>
      <h2>可访问的平台工作区</h2>
      <ul v-if="state.workspaces.length"><li v-for="workspace in state.workspaces" :key="workspace.id"><a :href="workspaceURL(workspace.id)" :aria-current="state.selected?.id === workspace.id ? 'page' : undefined">{{ workspace.name }}</a><span class="muted"> · {{ workspace.slug }}</span></li></ul>
      <p v-else>尚无可访问工作区，请在平台中创建或申请加入。</p>
      <p v-if="state.selected">当前工作区：<strong>{{ state.selected.name }}</strong></p>
    </section>
    <div class="actions" v-if="state.status !== 'loading' && state.status !== 'logging_out'">
      <button type="button" @click="refresh">重新验证会话</button>
      <button v-if="state.error?.code === 'LOGOUT_FAILED'" type="button" @click="controller.logout()">重试退出</button>
      <a v-if="state.status === 'anonymous'" :href="signIn">前往平台登录</a>
    </div>
    <aside><h2>农业业务暂未开放</h2><p>当前交付为可信平台会话与工作区接入。地块、影像分析、外业、报告和农业 Agent，需完成服务端授权与租户隔离后开放。</p><p>本页面不调用旧 GIS 业务接口，不加载底图、影像或三维资源，也不展示模拟农业结果。</p></aside>
  </main>
</template>
