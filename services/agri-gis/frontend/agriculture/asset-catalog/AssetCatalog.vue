<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, shallowRef, watch } from 'vue'
import { AssetController } from './controller'
import MetadataEditor from './MetadataEditor.vue'
import { kinds, timestamp, type Metadata } from './metadata'
import { grantsFrom, type Capability, type Grant } from './types'
const props = defineProps<{ workspace: string; checking: boolean }>()
const emit = defineEmits<{ revalidate: []; dirty: [value: boolean] }>()
const controller = new AssetController(props.workspace, undefined, () => emit('revalidate'))
const state = shallowRef(controller.state); controller.onChange = value => { state.value = value }
const mode = ref<'view' | 'create' | 'version'>('view'), dirty = ref(false), kind = ref(''), dataClass = ref(''), version = ref(1), operationID = ref(''), message = ref(''), expired = ref(false)
const grants = reactive<Record<string, { capabilities: Capability[]; expires: string }>>({})
const historical = computed(() => !!state.value.selected && state.value.selected.version !== state.value.selected.revision)
let timer: ReturnType<typeof setInterval> | undefined
watch(() => state.value.context, context => { for (const member of context?.eligible_grantees ?? []) grants[member.user_id] ??= { capabilities: [], expires: '' } })
watch(() => [dirty.value, state.value.uncertain, state.value.operation?.state], () => emit('dirty', dirty.value || controller.unresolved))
watch(() => state.value.operation?.state, value => { if (value === 'completed') dirty.value = false })
onMounted(() => { controller.setChecking(props.checking); if (!props.checking) void controller.initialize(); timer = setInterval(() => { expired.value = !!state.value.preview && Date.parse(state.value.preview.expires_at) <= Date.now() }, 1000) })
watch(() => props.checking, checking => { controller.setChecking(checking); if (!checking && !state.value.context && !state.value.error) void controller.initialize() })
watch(() => props.workspace, () => controller.dispose())
onUnmounted(() => { clearInterval(timer); controller.dispose() })
function discard() { return !dirty.value || window.confirm('放弃未提交的资产声明草稿？已提交登记与历史版本不会撤销或删除。') }
function create() { if (controller.locked || controller.unresolved || !discard()) return; controller.create(); mode.value = 'create'; dirty.value = false; message.value = ''; for (const member of Object.values(grants)) { member.capabilities = []; member.expires = '' } }
async function open(id: string, selectedVersion: number) { if (controller.locked || controller.unresolved || !discard()) return; await controller.select(id, selectedVersion); mode.value = 'view'; dirty.value = false; version.value = state.value.selected?.version ?? 1; message.value = '' }
function changed() { dirty.value = true; controller.draftChanged(); message.value = '' }
function prepare(metadata: Metadata) {
  try {
    if (mode.value === 'create') {
      const plan: Grant[] = (state.value.context?.eligible_grantees ?? []).filter(m => grants[m.user_id]?.capabilities.length).map(m => { const g = grants[m.user_id]!; if (g.expires && (!timestamp(g.expires) || Date.parse(g.expires) <= Date.now())) throw Error('授权到期时间必须是带时区的明确未来时间。'); return { subject_user_id: m.user_id, capabilities: [...g.capabilities], ...(g.expires ? { expires_at: g.expires } : {}) } })
      if (!plan.length) throw Error('请显式选择至少一位已有业务角色成员及初始能力；不会自动授权创建人。')
      void controller.prepare({ action: 'create', expected_revision: 0, metadata, initial_grants: grantsFrom(plan) })
    } else if (state.value.selected) void controller.prepare({ action: 'version', asset_id: state.value.selected.asset_id, expected_revision: state.value.selected.revision, metadata })
  } catch (error) { message.value = error instanceof Error ? error.message : '初始授权计划无效。' }
}
function adopt() { if (!window.confirm('丢弃旧草稿，使用服务器当前元数据版本重新编辑？历史不变。')) return; controller.adoptComparison(); mode.value = 'version'; dirty.value = false }
</script>
<template>
  <section class="farm-workspace" data-testid="asset-catalog" :aria-busy="state.busy">
    <h2>资产与数据权利目录</h2><p>登记声明与实体文件分离。许可声明不是批准，演示数据不能混入正式业务。每次实际使用仍需当前权利和实体状态检查。</p>
    <p v-if="checking" role="status">正在复核会话权限，暂时禁用写入。</p><p v-if="state.busy" role="status" data-testid="asset-busy">正在请求真实资产目录服务…</p>
    <div v-if="state.error" role="alert" data-testid="asset-error" :data-error-code="state.error.code"><p>{{ state.error.message }}</p><code>{{ state.error.code }}</code><button v-if="!state.context" :disabled="controller.locked" @click="controller.initialize()">重新读取当前权限</button></div>
    <p v-if="message" role="alert" data-testid="asset-editor-error">{{ message }}</p>
    <section v-if="state.safeReceipt" data-testid="asset-safe-receipt"><h3>已重新校验的本人操作回执</h3><code>{{ state.safeReceipt.operation_id }}</code><p>{{ state.safeReceipt.state }}</p><code>{{ state.safeReceipt.status_url }}</code><p>资产内容权限已不可用，元数据与旧授权已清除。版本推进后直接授权可能需要管理员通过现有授权流程明确重签；不会自动授予新版本访问权。此回执不代表内容权限或实体 ready。</p></section>
    <template v-if="state.context">
      <form class="farm-search" @submit.prevent="controller.list(kind, dataClass)"><label>资产类别<select v-model="kind" name="asset_filter_kind"><option value="">所有获授权类别</option><option v-for="value in kinds" :key="value" :value="value">{{ value }}</option></select></label><label>数据等级<select v-model="dataClass" name="asset_filter_class"><option value="">所有获授权等级</option><option value="demo">演示</option><option value="operational">正式业务</option></select></label><button data-testid="asset-search" :disabled="controller.locked || controller.unresolved">查询获授权目录</button><button v-if="state.context.can_register_without_farm || state.context.farms.length" type="button" data-testid="asset-create" :disabled="controller.locked || controller.unresolved" @click="create">登记新资产元数据</button></form>
      <p v-if="!state.page.items.length && !state.busy" data-testid="asset-empty">当前筛选下没有获授权资产；治理身份不自动具有内容读取权限。</p>
      <ul class="farm-list" data-testid="asset-list"><li v-for="asset in state.page.items" :key="asset.asset_id" :data-asset-id="asset.asset_id"><button :disabled="controller.locked || controller.unresolved" @click="open(asset.asset_id, asset.version)">{{ asset.name }}</button><span>{{ asset.kind }} · {{ asset.data_class }} · v{{ asset.version }} · {{ asset.status }} · 权利快照 {{ asset.rights.status }}</span></li></ul>
      <button v-if="state.page.has_more" :disabled="controller.locked || controller.unresolved" @click="controller.list(state.kind, state.dataClass, true)">加载下一页资产</button>
      <details><summary>恢复查询持久资产操作</summary><form @submit.prevent="controller.lookup(operationID)"><input v-model="operationID" name="asset_operation_id" required maxlength="124" placeholder="aop_…"><button :disabled="controller.locked || controller.unresolved">查询原登记操作</button></form></details>
      <article v-if="state.selected" data-testid="asset-detail" :data-version="state.selected.version" :data-revision="state.selected.revision">
        <h3>{{ state.selected.name }} · {{ state.selected.data_class }}</h3><dl class="farm-facts"><dt>稳定资产 / 版本身份</dt><dd>{{ state.selected.asset_id }} / {{ state.selected.asset_version_id }}</dd><dt>选中版本 / 当前头</dt><dd>{{ state.selected.version }} / {{ state.selected.revision }}</dd><dt>所属农场</dt><dd>{{ state.selected.farm_id ?? '无所属农场' }}</dd><dt>当前资产状态</dt><dd>{{ state.selected.status }}</dd><dt>所选版本实体</dt><dd>{{ state.selected.entity ? '存在受信实体引用；访问仍需独立授权和当前权利检查' : '尚无实体绑定：只有元数据，不可用于正式处理' }}</dd><dt>权利快照</dt><dd>{{ state.selected.rights.status }} · {{ state.selected.rights.review_ref ?? '无审核引用' }}</dd><dt>权利快照 SHA256</dt><dd>{{ state.selected.rights.snapshot_hash }}</dd><dt>元数据 SHA256</dt><dd>{{ state.selected.metadata_hash }}</dd><dt>版本创建时间</dt><dd>{{ state.selected.created_at }}</dd></dl>
        <p>approved 仅表示该版本的历史权利快照；不覆盖当前来源撤回、到期、用途或敏感性限制。public 标签不授予分发权限。</p>
        <pre data-testid="asset-metadata">{{ JSON.stringify(state.selected.metadata, null, 2) }}</pre>
        <p v-if="historical" data-testid="asset-historical">历史版本只读，不能覆盖；版本写入必须基于当前授权与当前版本。</p>
        <button v-if="!historical && mode === 'view' && state.selected.allowed_actions.includes('agri.asset.create')" data-testid="asset-version-edit" :disabled="controller.locked || controller.unresolved" @click="mode = 'version'">追加新的元数据版本</button>
        <form @submit.prevent="open(state.selected.asset_id, version)"><label>读取不可变历史版本<input v-model.number="version" type="number" name="asset_history_version" required min="1" :max="state.selected.revision" step="1"></label><button data-testid="asset-history-read" :disabled="controller.locked || controller.unresolved">按当前权限读取该版本</button></form>
      </article>
      <section v-if="mode === 'create'" data-testid="asset-initial-grants"><h3>显式初始授权</h3><p>从当前平台列举的已有业务角色成员中选择。内容能力不包含任何权利批准能力。</p><fieldset :disabled="controller.locked || controller.unresolved || state.operation?.state === 'completed'" @input="changed"><div v-for="member in state.context.eligible_grantees.filter(m => m.allowed_capabilities.length)" :key="member.user_id" class="farm-grant"><strong>{{ member.display_name }}</strong><code>{{ member.user_id }}</code><label v-for="cap in member.allowed_capabilities" :key="cap" class="farm-checkbox"><input v-model="grants[member.user_id]!.capabilities" type="checkbox" :value="cap">{{ cap }}</label><label>授权到期时间（可选、包含时区）<input v-model="grants[member.user_id]!.expires" placeholder="2027-01-01T00:00:00Z"></label></div></fieldset></section>
      <MetadataEditor v-if="mode === 'create' || (mode === 'version' && state.selected && !historical)" :key="`${mode}:${state.editorEpoch}`" :initial="mode === 'create' ? null : state.selected?.metadata ?? null" :farms="state.context.farms.map(f => ({ id: f.farm_id, name: f.name }))" :sources="state.context.sources" :can-unscoped="state.context.can_register_without_farm" :disabled="controller.locked || controller.unresolved || state.operation?.state === 'completed'" @dirty="changed" @prepare="prepare" />
      <section v-if="state.conflict" data-testid="asset-conflict"><h3>版本冲突：草稿已保留</h3><button v-if="state.selected" :disabled="controller.locked" @click="controller.compare()">重新读取当前版本比较</button><button v-else :disabled="controller.locked || controller.unresolved" @click="controller.refreshContext()">重新读取当前登记范围和成员，不自动重试</button><template v-if="state.comparison"><pre>{{ JSON.stringify(state.comparison.metadata, null, 2) }}</pre><button :disabled="controller.locked" @click="adopt">明确丢弃旧草稿并基于当前版本编辑</button></template></section>
      <section v-if="state.preview" data-testid="asset-operation-preview"><h3>确认具体登记或版本操作</h3><p>{{ state.preview.action }} · {{ state.preview.asset_id || '新稳定资产' }} · 预期版本 {{ state.preview.expected_revision }} · 截止 {{ state.preview.expires_at }}</p><p>参数 SHA256 {{ state.preview.request_hash }}</p><pre data-testid="asset-preview-summary">{{ JSON.stringify({ ...state.preview.summary, initial_grants: state.preview.initial_grants }, null, 2) }}</pre><p>实体为 null，状态 validating；确认不会把声明标为 ready 或批准用途。</p><button v-if="!state.operation || state.operation.state === 'preview'" data-testid="asset-confirm" :disabled="controller.locked || expired" @click="controller.confirm()">确认并提交同一份元数据与授权计划</button></section>
      <section v-if="state.operation || state.uncertain" data-testid="asset-operation" :data-operation-state="state.operation?.state ?? 'unknown'"><h3>持久登记操作 {{ state.operation?.state ?? '结果未知' }}</h3><code>{{ state.operation?.operation_id ?? state.preview?.preview_id }}</code><p v-if="state.operation?.state === 'completed'">元数据版本已登记，不等于实体已验证或允许使用。请刷新当前获授权目录。</p><p v-else>尚未确认完成；查询原操作，不能自行创建另一操作替代未知结果。</p><button data-testid="asset-operation-poll" :disabled="controller.locked" @click="controller.poll()">查询原操作</button><button v-if="state.intent && state.preview && (state.uncertain || ['preview', 'pending'].includes(state.operation?.state ?? ''))" :disabled="controller.locked" @click="controller.confirm()">原参数和幂等键重试</button></section>
    </template>
  </section>
</template>
