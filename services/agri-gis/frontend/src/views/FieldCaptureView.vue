<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'

import MobileFieldCaptureForm from '@/components/field/mobile/MobileFieldCaptureForm.vue'
import { useFieldCaptureStore } from '@/store/fieldCaptureStore'
import { useUserStore } from '@/store/userStore'
import { legacyDemoEnabled, explicitDemoContext } from '@/security/legacyIdentity'

const route = useRoute()
const captureStore = useFieldCaptureStore()
const userStore = useUserStore()

onMounted(async () => {
  document.documentElement.classList.add('mobile-standalone')
  const projectCode = explicitDemoContext(legacyDemoEnabled(), route.query.project_code)
  const taskCode = explicitDemoContext(legacyDemoEnabled(), route.query.task_code)
  if (!projectCode || !taskCode) return
  captureStore.setTaskCode(taskCode)
  captureStore.startNetworkMonitoring()
  await userStore.initialize(projectCode)
})

onUnmounted(() => {
  captureStore.stopNetworkMonitoring()
  document.documentElement.classList.remove('mobile-standalone')
})
</script>

<template>
  <div class="field-capture-view">
    <MobileFieldCaptureForm v-if="legacyDemoEnabled()" />
    <p v-else role="status">外业领域接口尚未接入可信平台身份，采集暂不可用。</p>
  </div>
</template>

<style scoped>
.field-capture-view { min-height: 100vh; padding: 14px 0 36px; background: #eef3f0; }
</style>
