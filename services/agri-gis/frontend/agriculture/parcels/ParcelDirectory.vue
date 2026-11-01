<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import { FarmError } from '../farms/transport'
import { ParcelAPI } from './api'
import ParcelWorkspace from './ParcelWorkspace.vue'
import { contextKey, selectedContext } from './directory'
import type { ContextPage, ParcelContext } from './types'
const props = defineProps<{ workspace: string; checking: boolean }>()
const emit = defineEmits<{ revalidate: []; dirty: [value: boolean] }>()
const api = new ParcelAPI()
const page = shallowRef<ContextPage>({ items: [], next_cursor: '', has_more: false })
const selected = shallowRef<ParcelContext | null>(null)
const error = shallowRef<FarmError | null>(null)
const busy = ref(false), loaded = ref(false), dirty = ref(false)
const selectedKey = computed(() => selected.value ? contextKey(selected.value) : '')
let active: AbortController | null = null, generation = 0, disposed = false
watch(dirty, value => emit('dirty', value))
function clear() { page.value = { items: [], next_cursor: '', has_more: false }; selected.value = null; dirty.value = false }
function close() { if (!dirty.value || window.confirm('放弃未提交的地块草稿？已提交操作不会撤销。')) { selected.value = null; dirty.value = false } }
function choose(parent: ParcelContext) {
  if (selected.value?.farm_id === parent.farm_id) return
  if (busy.value || props.checking || disposed || (dirty.value && !window.confirm('切换农场会放弃本地未提交草稿；已提交操作不会撤销。'))) return
  selected.value = parent; dirty.value = false
}
async function load(more = false) {
  if (busy.value || props.checking || disposed || (more && !page.value.has_more)) return
  active?.abort(); const request = new AbortController(); active = request; const version = ++generation
  busy.value = true; error.value = null
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    const deadline = new Promise<never>((_resolve, reject) => { timer = setTimeout(() => { request.abort(); reject(new FarmError(0, 'TIMEOUT', '授权地块目录读取超时，请重新读取。')) }, 20000) })
    const selectedBefore = selected.value
    const work = async () => {
      const result = await api.farms(props.workspace, request.signal, more ? page.value.next_cursor : '')
      request.signal.throwIfAborted()
      const current = await selectedContext(api, props.workspace, selectedBefore, result.items, more, request.signal)
      return { result, current }
    }
    const { result, current } = await Promise.race([work(), deadline])
    if (disposed || generation !== version || request.signal.aborted) return
    const items = more ? [...page.value.items, ...result.items.filter(v => !page.value.items.some(old => old.farm_id === v.farm_id))] : result.items
    page.value = { ...result, items }; loaded.value = true
    if (selected.value) {
      // Page omission is not revocation; an independently checked selected
      // context may remain outside this page. Boundary changes preserve drafts.
      if (!current || contextKey(current) !== contextKey(selected.value)) dirty.value = false
      selected.value = current
    }
  } catch (failure) {
    if (disposed || generation !== version) return
    error.value = failure instanceof FarmError ? failure : new FarmError(0, 'NETWORK_ERROR', '无法确认授权地块目录；不会按输入猜测或扫描其他农场。')
    if ([401, 403, 404].includes(error.value.status)) { const status = error.value.status; clear(); if (status !== 404) emit('revalidate') }
  } finally { clearTimeout(timer); if (!disposed && generation === version) busy.value = false }
}
onMounted(() => { if (!props.checking) void load() })
watch(() => props.checking, value => { if (!value && !loaded.value && !error.value) void load() })
watch(() => props.workspace, () => { disposed = true; ++generation; active?.abort(); clear() })
onUnmounted(() => { disposed = true; ++generation; active?.abort(); clear() })
</script>
<template>
  <section data-testid="parcel-directory" :aria-busy="busy">
    <h2>我获授权的地块范围</h2>
    <p>目录来自当前服务端显式授权。直接地块授权可在这里进入，无需获得父农场内容读取权；不显示未获授权农场名称。</p>
    <p v-if="busy" role="status" data-testid="parcel-directory-busy">正在重新读取当前授权目录…</p>
    <p v-if="checking" role="status">会话与工作区权限正在复核，操作暂时禁用。</p>
    <p v-if="error" role="alert" data-testid="parcel-directory-error" :data-error-code="error.code">{{ error.message }}</p>
    <button type="button" data-testid="parcel-directory-refresh" :disabled="busy || checking" @click="load()">刷新真实授权范围</button>
    <p v-if="loaded && !page.items.length && !busy" data-testid="parcel-directory-empty">当前没有获授权的地块范围；治理身份不自动授予业务内容权限。</p>
    <ul data-testid="parcel-directory-list"><li v-for="parent in page.items" :key="parent.farm_id"><button type="button" :disabled="busy || checking" :aria-pressed="selected?.farm_id === parent.farm_id" @click="choose(parent)">{{ parent.farm_id }}</button><span>{{ parent.expected_farm_revision ? `获授权父版本 ${parent.expected_farm_revision}` : '直接地块授权范围' }}</span></li></ul>
    <button v-if="page.has_more" type="button" data-testid="parcel-directory-more" :disabled="busy || checking" @click="load(true)">加载下一页授权范围</button>
    <template v-if="selected"><button type="button" data-testid="parcel-directory-close" :disabled="busy || checking" @click="close">关闭地块工作台</button><ParcelWorkspace :key="selectedKey" :workspace="workspace" :farm-i-d="selected.farm_id" :checking="checking || busy" @dirty="dirty = $event" @revalidate="emit('revalidate')" /></template>
  </section>
</template>
