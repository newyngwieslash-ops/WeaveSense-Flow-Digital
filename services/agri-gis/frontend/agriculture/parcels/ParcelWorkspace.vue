<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import FarmBoundaryMap from '../farms/FarmBoundaryMap.vue'
import ParcelEditor from './ParcelEditor.vue'
import { ParcelController } from './parcel-controller'
import { date, type Intent, type Selector } from './types'
const props = defineProps<{ workspace: string; farmID: string; checking: boolean }>()
const emit = defineEmits<{ revalidate: []; dirty: [value: boolean] }>()
const controller = new ParcelController(props.workspace, props.farmID, undefined, () => emit('revalidate'))
const state = shallowRef(controller.state)
controller.onChange = value => { state.value = value }
const mode = ref<'view' | 'create' | 'edit'>('view'), dirty = ref(false), at = ref(''), lookupID = ref(''), message = ref(''), expired = ref(false)
const historical = computed(() => !!state.value.selected && state.value.selected.geometry_revision !== state.value.selected.current_geometry_revision)
let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => { controller.setChecking(props.checking); if (!props.checking) void controller.initialize(); timer = setInterval(() => { expired.value = !!state.value.preview && Date.parse(state.value.preview.expires_at) <= Date.now() }, 1000) })
watch(() => props.checking, checking => { controller.setChecking(checking); if (!checking && !state.value.context && !state.value.error) void controller.initialize() })
watch(() => [props.workspace, props.farmID], () => controller.dispose())
watch(() => [dirty.value, state.value.uncertain, state.value.operation?.state], () => emit('dirty', dirty.value || controller.unresolved))
watch(() => state.value.operation?.state, value => { if (value === 'completed') dirty.value = false })
onUnmounted(() => { clearInterval(timer); controller.dispose() })
function discard() { return !dirty.value || window.confirm('放弃本地未提交的边界草稿？已提交操作和历史版本不会删除。') }
function create() { if (controller.locked || controller.unresolved || !discard()) return; controller.create(); mode.value = 'create'; dirty.value = false; message.value = '' }
async function open(id: string, selector?: Selector) { if (controller.locked || controller.unresolved || !discard()) return; await controller.select(id, selector); mode.value = 'view'; dirty.value = false; message.value = '' }
function changed() { dirty.value = true; controller.draftChanged() }
function prepare(intent: Intent) { void controller.prepare(intent) }
function atTime() {
  if (!date(at.value)) { message.value = '请输入带 Z 的完整 UTC 时间，如 2026-11-01T10:00:00.123456Z。'; return }
  if (state.value.selected) void open(state.value.selected.id, { at: at.value })
}
function adopt() {
  if (!window.confirm('丢弃旧草稿并使用服务器当前边界重新编辑？历史版本不会改写。')) return
  controller.adoptComparison(); mode.value = 'edit'; dirty.value = false
}
</script>
<template>
  <section data-testid="parcel-workspace" :aria-busy="state.busy" :data-farm-id="farmID">
    <h2>稳定地块与不可变边界版本</h2>
    <p>父农场 {{ farmID }}。只有当前明确授权可读取或修改；创建完成不代表自动获得内容权限。</p>
    <p v-if="checking" role="status">正在重新验证当前会话与工作区，暂不允许写入。</p>
    <p v-if="state.busy" role="status" data-testid="parcel-busy">正在请求真实地块服务…</p>
    <div v-if="state.error" role="alert" data-testid="parcel-error" :data-error-code="state.error.code"><p>{{ state.error.message }}</p><code>{{ state.error.code }}</code><button v-if="!state.context" :disabled="controller.locked" @click="controller.initialize()">重新读取当前权限</button></div>
    <p v-if="message" role="alert" data-testid="parcel-selector-error">{{ message }}</p>
    <template v-if="state.context">
      <p data-testid="parcel-context" :data-farm-revision="state.context.expected_farm_revision">{{ state.context.expected_farm_revision ? `父农场当前版本 ${state.context.expected_farm_revision}` : '仅有直接地块授权，没有父农场创建权限' }}</p>
      <button v-if="state.context.allowed_actions.includes('agri.parcel.create')" type="button" data-testid="parcel-create" :disabled="controller.locked || controller.unresolved" @click="create">创建稳定地块</button>
      <button v-if="state.context.allowed_actions.includes('agri.parcel.read')" type="button" data-testid="parcel-refresh" :disabled="controller.locked || controller.unresolved" @click="controller.list()">刷新获授权地块</button>
      <p v-if="!state.context.allowed_actions.includes('agri.parcel.read')">当前没有父农场地块读取权限；不将缺少授权显示为“没有地块”。</p>
      <p v-else-if="!state.page.items.length && !state.busy" data-testid="parcel-empty">当前范围没有可读取的地块。</p>
      <ul data-testid="parcel-list"><li v-for="parcel in state.page.items" :key="parcel.id" :data-parcel-id="parcel.id"><button type="button" :disabled="controller.locked || controller.unresolved" @click="open(parcel.id)">{{ parcel.name }}</button> {{ parcel.external_code }} · {{ parcel.data_class }} · 边界 v{{ parcel.geometry_revision }} · {{ parcel.area_m2 }} m²</li></ul>
      <button v-if="state.page.has_more" :disabled="controller.locked || controller.unresolved" @click="controller.list(true)">加载下一页地块</button>
      <details><summary>查询已提交的地块操作</summary><p>刷新不撤销后台提交。使用此前回执的操作编号查询，不在浏览器存储边界、原因或凭据。</p><form @submit.prevent="controller.lookup(lookupID)"><input v-model="lookupID" name="parcel_operation_id" required maxlength="124" placeholder="pop_…"><button :disabled="controller.locked || controller.unresolved">查询持久操作</button></form></details>
      <article v-if="state.selected" data-testid="parcel-detail" :data-revision="state.selected.geometry_revision" :data-current-revision="state.selected.current_geometry_revision">
        <h3>{{ state.selected.name }} · {{ state.selected.data_class }}</h3>
        <dl><dt>稳定身份</dt><dd>{{ state.selected.id }}</dd><dt>业务编号</dt><dd>{{ state.selected.external_code }}</dd><dt>选中 / 当前边界版本</dt><dd>{{ state.selected.geometry_revision }} / {{ state.selected.current_geometry_revision }}</dd><dt>有效区间（UTC，前闭后开）</dt><dd>{{ state.selected.valid_from }} 至 {{ state.selected.valid_to ?? '当前' }}</dd><dt>地块创建时间</dt><dd>{{ state.selected.created_at }}</dd><dt>可信操作者</dt><dd>{{ state.selected.actor_user_id }}</dd><dt>变更原因</dt><dd>{{ state.selected.reason }}</dd><dt>几何 SHA256</dt><dd>{{ state.selected.geometry_hash }}</dd><dt>权威面积</dt><dd>{{ state.selected.area_m2 }} m² / {{ state.selected.area_ha }} ha / {{ state.selected.area_mu }} 亩</dd><dt>坐标来源</dt><dd>{{ state.selected.source_crs }} · {{ state.selected.coordinate_order }}</dd></dl>
        <p>派生产物依赖尚未接入：影响数量未知，不代表没有影响。此次变更不覆盖旧几何或历史结果空间引用。</p>
        <p v-if="typeof state.selected.provenance.area_algorithm === 'string'">面积算法：{{ state.selected.provenance.area_algorithm }}</p>
        <p v-if="typeof state.selected.provenance.algorithm_version === 'string'">算法版本：{{ state.selected.provenance.algorithm_version }}</p>
        <p v-if="historical" data-testid="parcel-historical">历史版本只读。恢复与更完整的编辑工作流由后续功能提供，不会原地改写历史。</p>
        <FarmBoundaryMap v-if="mode === 'view'" :geometry="state.selected.geometry" />
        <button v-if="!historical && state.selected.allowed_actions.includes('agri.parcel.update') && mode === 'view'" type="button" data-testid="parcel-edit" :disabled="controller.locked || controller.unresolved" @click="mode = 'edit'">编辑边界并新增版本</button>
        <button type="button" data-testid="parcel-history-load" :disabled="controller.locked || controller.unresolved" @click="controller.history()">读取当前获授权的版本历史</button>
        <button v-if="historical" type="button" data-testid="parcel-current" :disabled="controller.locked || controller.unresolved" @click="open(state.selected.id)">重新读取当前版本</button>
        <form @submit.prevent="atTime"><label>历史 UTC 时间<input v-model="at" name="parcel_at" required placeholder="2026-11-01T10:00:00.123456Z"></label><button data-testid="parcel-at-submit" :disabled="controller.locked || controller.unresolved">查询该时间有效边界</button></form>
        <ul data-testid="parcel-history"><li v-for="revision in state.history.items" :key="revision.geometry_revision"><button type="button" :disabled="controller.locked || controller.unresolved" @click="open(revision.id, { revision: revision.geometry_revision })">边界版本 {{ revision.geometry_revision }}</button> {{ revision.valid_from }} · {{ revision.reason }} · {{ revision.geometry_hash }}</li></ul>
        <button v-if="state.history.has_more" :disabled="controller.locked || controller.unresolved" @click="controller.history(true)">加载下一页历史</button>
      </article>
      <ParcelEditor v-if="mode === 'create' || (mode === 'edit' && state.selected && !historical)" :key="`${mode}:${state.editorEpoch}`" :parcel="mode === 'create' ? null : state.selected" :farm-i-d="farmID" :farm-revision="state.context.expected_farm_revision" :disabled="controller.locked || controller.unresolved || state.operation?.state === 'completed'" @dirty="changed" @prepare="prepare" />
      <section v-if="state.conflict" data-testid="parcel-conflict"><h3>边界版本冲突：未覆盖服务器版本</h3><p>上方草稿保留。先读取当前版本比较，再明确决定是否丢弃草稿重新编辑。</p><button v-if="state.selected" :disabled="controller.locked" @click="controller.compare()">比较服务器当前版本</button><button v-else data-testid="parcel-parent-refresh" :disabled="controller.locked || controller.unresolved" @click="controller.refreshContext()">重新读取父农场版本与权限，保留创建草稿；不会自动重试</button><template v-if="state.comparison"><p>当前版本 {{ state.comparison.geometry_revision }} · {{ state.comparison.geometry_hash }}</p><FarmBoundaryMap :geometry="state.comparison.geometry" /><button :disabled="controller.locked" @click="adopt">明确丢弃旧草稿，基于当前版本重新编辑</button></template></section>
      <section v-if="state.preview" data-testid="parcel-operation-preview"><h3>确认具体边界操作</h3><p>{{ state.preview.action }} · 父农场 {{ state.preview.farm_id }} · 地块 {{ state.preview.parcel_id || '新身份' }} · 预期边界版本 {{ state.preview.expected_revision }}</p><p>有效至 {{ state.preview.expires_at }} · 参数 SHA256 {{ state.preview.request_hash }}</p><pre data-testid="parcel-preview-summary">{{ JSON.stringify(state.preview.summary, null, 2) }}</pre><button v-if="!state.operation || state.operation.state === 'preview'" type="button" data-testid="parcel-confirm" :disabled="controller.locked || expired" @click="controller.confirm()">确认并提交相同边界与原因</button><p v-if="expired && !state.operation">预览已过期，请重新生成；结果未知的已提交操作应查询原回执。</p></section>
      <section v-if="state.operation || state.uncertain" data-testid="parcel-operation" :data-operation-state="state.operation?.state ?? 'unknown'"><h3>持久操作：{{ state.operation?.state ?? '结果未知' }}</h3><code>{{ state.operation?.operation_id ?? state.preview?.preview_id }}</code><p v-if="state.operation?.state === 'completed'">服务器确认边界版本 {{ state.operation.geometry_revision }} 已提交。请刷新获授权列表；不自动授予内容权限。</p><p v-else>尚未确认完成。查询原操作，不以另一操作替代未知结果。</p><button type="button" data-testid="parcel-operation-poll" :disabled="controller.locked" @click="controller.poll()">查询原操作</button><button v-if="state.intent && state.preview && (state.uncertain || ['preview', 'pending'].includes(state.operation?.state ?? ''))" :disabled="controller.locked" @click="controller.confirm()">用原参数与幂等键重试</button></section>
    </template>
  </section>
</template>
