<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'

import MobileUavCaptureForm from '@/components/uav/mobile/MobileUavCaptureForm.vue'
import { useUavCaptureStore } from '@/store/uavCaptureStore'
import { useUserStore } from '@/store/userStore'
import { legacyDemoEnabled, explicitDemoContext } from '@/security/legacyIdentity'

const route = useRoute()
const captureStore = useUavCaptureStore()
const userStore = useUserStore()

onMounted(async () => {
  document.documentElement.classList.add('mobile-standalone')
  const projectCode = explicitDemoContext(legacyDemoEnabled(), route.query.project_code)
  if (!projectCode) return
  captureStore.setProjectCode(projectCode)
  captureStore.startNetworkMonitoring()
  await userStore.initialize(projectCode)
  try {
    await captureStore.load()
  } catch {
    // 请求层和移动表单会显示安全错误状态。
  }
})

onUnmounted(() => {
  captureStore.stopNetworkMonitoring()
  document.documentElement.classList.remove('mobile-standalone')
})
</script>

<template>
  <div class="uav-capture-view">
    <MobileUavCaptureForm v-if="legacyDemoEnabled()" />
    <p v-else role="status">无人机领域接口尚未接入可信平台身份，采集暂不可用。</p>
  </div>
</template>

<style scoped>
.uav-capture-view { min-height: 100vh; padding: 14px 0 36px; background: #edf2f0; }
</style>
