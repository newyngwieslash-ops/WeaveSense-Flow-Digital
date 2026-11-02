<script setup lang="ts">
import { onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import FarmEditor from './FarmEditor.vue'
import FarmBoundaryMap from './FarmBoundaryMap.vue'
import ParcelWorkspace from '../parcels/ParcelWorkspace.vue'
import { FarmController } from './farm-controller'
import type { FarmSummary, Intent } from './types'
const props = defineProps<{ workspace: string; checking: boolean }>()
const emit = defineEmits<{ revalidate: [] }>()
const controller = new FarmController(props.workspace, undefined, () => emit('revalidate'))
const state = shallowRef(controller.state)
controller.onChange = value => { state.value = value }
const mode = ref<'view' | 'edit' | 'create'>('view')
const dirty = ref(false)
const search = ref('')
const status = ref<'active' | 'archived'>('active')
const reason = ref('')
const recoveryID = ref('')
const expired = ref(false)
const showParcels = ref(false)
const parcelDirty = ref(false)
let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  controller.setChecking(props.checking); void controller.initialize()
  timer = setInterval(() => { expired.value = !!state.value.preview && Date.parse(state.value.preview.expires_at) <= Date.now() }, 1000)
})
watch(() => props.checking, checking => controller.setChecking(checking))
onUnmounted(() => { clearInterval(timer); controller.dispose() })
function discard() { return !(dirty.value || parcelDirty.value) || window.confirm('放弃尚未提交的本地草稿？已提交操作不会撤销，操作记录不会删除。') }
function toggleParcels() {
  if (showParcels.value && !discard()) return
  showParcels.value = !showParcels.value; parcelDirty.value = false
}
function editFarm() {
  if (!discard()) return
  showParcels.value = false; parcelDirty.value = false; mode.value = 'edit'
}
async function open(row: FarmSummary) {
  if (controller.locked || controller.unresolved || !discard()) return
  await controller.select(row.id); mode.value = 'view'; dirty.value = false; reason.value = ''
  showParcels.value = false
  parcelDirty.value = false
}
function create() {
  if (controller.locked || controller.unresolved || !discard()) return
  controller.create(); mode.value = 'create'; dirty.value = false; reason.value = ''
  showParcels.value = false; parcelDirty.value = false
}
function changed() { dirty.value = true; controller.draftChanged() }
function prepare(intent: Intent) { void controller.prepare(intent) }
function adopt() {
  if (!window.confirm('用服务器当前版本重新编辑？原草稿将被丢弃，请先自行比较。')) return
  controller.adoptComparison(); mode.value = 'edit'; dirty.value = false
}
function archive() {
  const farm = state.value.selected
  if (!farm || !reason.value.trim() || controller.locked || controller.unresolved || !discard()) return
  void controller.prepare({ action: 'archive', farm_id: farm.id, expected_revision: farm.revision, reason: reason.value })
}
</script>
<template>
  <section class="farm-workspace" data-testid="farm-workspace" :data-workspace-id="workspace" :aria-busy="state.busy">
    <h2>农场与经营区域</h2>
    <p>仅列出当前账号获明确授权的农场。初建是治理操作，不自动授予创建人内容访问权限。</p>
    <p v-if="checking" role="status">正在重新验证会话与工作区权限，写入暂时禁用。</p>
    <p v-if="state.busy" role="status" data-testid="farm-busy">正在请求服务器，请勿重复提交…</p>
    <div v-if="state.error" role="alert" data-testid="farm-error" :data-error-code="state.error.code">
      <p>{{ state.error.message }}</p><code>{{ state.error.code }}</code><small v-if="state.error.requestID"> 请求编号 {{ state.error.requestID }}</small>
      <button v-if="!state.context" type="button" :disabled="controller.locked" @click="controller.initialize()">重试加载</button>
    </div>
    <template v-if="state.context">
      <form class="farm-search" @submit.prevent="controller.list(status, search)">
        <label>名称或编号<input v-model="search" name="farm_search" maxlength="200"></label>
        <label>状态<select v-model="status" name="farm_status"><option value="active">在用</option><option value="archived">已归档（历史只读）</option></select></label>
        <button :disabled="controller.locked || controller.unresolved" data-testid="farm-search">查询</button>
        <button v-if="state.context.can_create" type="button" :disabled="controller.locked || controller.unresolved" data-testid="farm-create" @click="create">创建农场</button>
      </form>
      <p v-if="!state.page.items.length && !state.busy" data-testid="farm-empty">当前筛选下没有获授权农场。空列表不代表能够查看其他成员的资源。</p>
      <ul class="farm-list" data-testid="farm-list">
        <li v-for="farm in state.page.items" :key="farm.id" :data-farm-id="farm.id">
          <button type="button" :disabled="controller.locked || controller.unresolved" @click="open(farm)">{{ farm.name }}</button>
          <span>{{ farm.external_code }} · {{ farm.region_code }} · {{ farm.area_m2 }} m²</span>
          <strong :class="['farm-data-class', farm.data_class]">{{ farm.data_class === 'demo' ? '演示数据' : '正式业务' }}</strong>
          <span>{{ farm.status === 'archived' ? '已归档 · 只读' : '在用' }} · v{{ farm.revision }}</span>
        </li>
      </ul>
      <button v-if="state.page.has_more" type="button" :disabled="controller.locked || controller.unresolved" @click="controller.list(state.status, state.search, true)">加载下一页</button>
      <details><summary>恢复查询持久操作</summary><p>刷新页面不会撤销已经提交的操作。输入此前回执的操作编号；服务器仍将校验当前主体权限。不在浏览器存储联系人、边界或授权凭据。</p><form @submit.prevent="controller.lookup(recoveryID)"><label>操作编号<input v-model="recoveryID" name="operation_id" required maxlength="124" pattern="fop_[A-Za-z0-9_-]{1,120}" placeholder="fop_…"></label><button :disabled="controller.locked || controller.unresolved">查询已提交操作</button></form></details>
      <article v-if="state.selected" data-testid="farm-detail">
        <h3>{{ state.selected.name }} <strong>{{ state.selected.data_class === 'demo' ? '演示数据' : '正式业务数据' }}</strong></h3>
        <dl class="farm-facts"><dt>编号 / 状态 / 版本</dt><dd>{{ state.selected.external_code }} / {{ state.selected.status }} / {{ state.selected.revision }}</dd><dt>经营主体</dt><dd>{{ state.selected.operator_name }}</dd><dt>区域与时区</dt><dd>{{ state.selected.region_code }} · {{ state.selected.timezone }}</dd><dt>服务器面积</dt><dd>{{ state.selected.area_m2 }} m² / {{ state.selected.area_ha }} ha / {{ state.selected.area_mu }} 亩</dd><dt>坐标来源</dt><dd>{{ state.selected.source_crs }} · {{ state.selected.coordinate_order }}</dd><dt>更新时间</dt><dd>{{ state.selected.updated_at }}</dd></dl>
        <FarmBoundaryMap v-if="mode === 'view'" :geometry="state.selected.geometry" />
        <button v-if="state.selected.status === 'active' && mode === 'view'" type="button" data-testid="parcel-workspace-toggle" :disabled="controller.locked" :aria-expanded="showParcels" @click="toggleParcels">{{ showParcels ? '关闭地块版本工作台' : '打开地块版本工作台' }}</button>
        <ParcelWorkspace v-if="showParcels && state.selected.status === 'active' && mode === 'view'" :key="`${workspace}:${state.selected.id}`" :workspace="workspace" :farm-i-d="state.selected.id" :checking="checking" @dirty="parcelDirty = $event" @revalidate="emit('revalidate')" />
        <button v-if="state.selected.status === 'active' && state.selected.allowed_actions.includes('agri.farm.update') && mode === 'view'" type="button" data-testid="farm-edit" :disabled="controller.locked || controller.unresolved" @click="editFarm">编辑当前版本</button>
        <form v-if="state.selected.status === 'active' && state.selected.allowed_actions.includes('agri.farm.archive') && mode === 'view'" class="farm-archive" @submit.prevent="archive">
          <label>归档原因<textarea v-model="reason" name="archive_reason" required maxlength="500" :disabled="controller.locked || controller.unresolved" @input="controller.draftChanged()"></textarea></label>
          <button type="submit" data-testid="farm-archive-preview" :disabled="controller.locked || controller.unresolved">预览归档影响（保留编号与历史）</button>
        </form>
      </article>
      <FarmEditor v-if="mode === 'create' || (mode === 'edit' && state.selected?.status === 'active')" :key="`${mode}:${state.editorEpoch}`" :farm="mode === 'create' ? null : state.selected" :context="state.context" :disabled="controller.locked || controller.unresolved || state.operation?.state === 'completed'" @dirty="changed" @prepare="prepare" />
      <section v-if="state.conflict" data-testid="farm-conflict"><h3>冲突：未覆盖服务器版本</h3><p>草稿仍在上方。请读取当前版本，逐项比较后决定是否重新编辑。</p><button v-if="state.selected" type="button" :disabled="controller.locked" @click="controller.compare()">读取服务器当前版本进行比较</button>
        <template v-if="state.comparison"><pre>{{ JSON.stringify(state.comparison, null, 2) }}</pre><button type="button" :disabled="controller.locked" @click="adopt">丢弃旧草稿，使用当前版本重新编辑</button></template>
      </section>
      <section v-if="state.preview" data-testid="farm-operation-preview"><h3>确认具体操作 · {{ state.preview.action }}</h3>
        <p>对象 {{ state.preview.farm_id || '新建农场' }} · 预期版本 {{ state.preview.expected_revision }} · 有效至 {{ state.preview.expires_at }}</p>
        <p>参数摘要 SHA256 <code>{{ state.preview.request_hash }}</code></p>
        <pre data-testid="farm-preview-summary">{{ JSON.stringify(state.preview.summary, null, 2) }}</pre>
        <p>以上为服务器校验后的字段、边界、面积及授权计划。确认将以当前平台会话提交同一份参数，不代表未来 Agent 审批或独立成果审核。</p>
        <button v-if="!state.operation || state.operation.state === 'preview'" type="button" data-testid="farm-confirm" :disabled="controller.locked || expired" @click="controller.confirm()">确认并提交此操作</button>
        <p v-if="expired && !state.operation">预览已到期；请重新生成。已提交但结果未知时应查询原操作，不能据此断言失败。</p>
      </section>
      <section v-if="state.operation || state.uncertain" data-testid="farm-operation" :data-operation-state="state.operation?.state ?? 'unknown'">
        <h3>持久操作状态：{{ state.operation?.state ?? '结果未知' }}</h3><code>{{ state.operation?.operation_id ?? state.preview?.preview_id }}</code>
        <p v-if="state.operation?.state === 'completed'">服务器确认操作完成。此回执不是农场内容授权；请刷新列表查看自己可访问的农场。</p>
        <p v-else>尚未得到完成确认。不要重新创建另一操作；使用原操作查询或原幂等请求重试。</p>
        <p v-if="state.operation?.error_code">错误代码 {{ state.operation.error_code }}</p>
        <button type="button" data-testid="farm-operation-poll" :disabled="controller.locked" @click="controller.poll()">查询原操作状态</button>
        <button v-if="state.intent && state.preview && (state.uncertain || state.operation?.state === 'pending' || state.operation?.state === 'preview')" type="button" data-testid="farm-operation-retry" :disabled="controller.locked" @click="controller.confirm()">用原参数及幂等键重试</button>
        <button v-if="state.operation?.state === 'completed'" type="button" :disabled="controller.locked" @click="controller.list(status, search)">刷新获授权列表</button>
      </section>
    </template>
  </section>
</template>
