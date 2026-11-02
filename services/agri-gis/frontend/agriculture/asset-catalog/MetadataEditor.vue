<script setup lang="ts">
import { reactive, ref } from 'vue'
import { kinds, sourceKinds, purposes, sensitivities, metadataFrom, successorFrom, type Metadata } from './metadata'
const props = defineProps<{ initial: Metadata | null; farms: { id: string; name: string }[]; sources: { source_id: string; kind: Metadata['source']['kind']; label: string }[]; canUnscoped: boolean; disabled: boolean }>()
const emit = defineEmits<{ prepare: [metadata: Metadata]; dirty: [] }>()
const original = props.initial
const fields = reactive({ name: original?.name ?? '', kind: original?.kind ?? '', data_class: original?.data_class ?? '', farm: original ? (original.farm_id ?? '__unscoped__') : '', source_kind: original?.source.kind ?? '', source_id: original?.source.source_id ?? '', source_reference: original?.source.reference ?? '', owner: original?.rights_declaration.owner ?? '', license_ref: original?.rights_declaration.license_ref ?? '', uses: [...(original?.rights_declaration.permitted_uses ?? [])], attribution_required: original?.rights_declaration.attribution.required ?? false, attribution_text: original?.rights_declaration.attribution.text ?? '', valid_from: original?.rights_declaration.valid_from ?? '', expiry: original?.rights_declaration.expiry ?? '', sensitivity: original?.sensitivity ?? '', retain_until: original?.retention.retain_until ?? '', legal_hold: original?.retention.legal_hold ?? false })
const error = ref('')
function dirty() { error.value = ''; emit('dirty') }
function sourceChanged() { if (!props.sources.some(s => s.source_id === fields.source_id && s.kind === fields.source_kind)) fields.source_id = ''; dirty() }
function prepare() {
  if (props.disabled) return
  try {
    if (!original && !(fields.farm === '__unscoped__' ? props.canUnscoped : props.farms.some(f => f.id === fields.farm))) throw Error('请选择当前真实授权的农场或明确允许的无农场登记。')
    if (fields.source_id && !props.sources.some(s => s.source_id === fields.source_id && s.kind === fields.source_kind)) throw Error('原来源已不在当前受审范围，请明确选择可用来源或未知来源；历史声明不会改写。')
    const value = { name: fields.name, kind: fields.kind, data_class: fields.data_class, farm_id: fields.farm === '__unscoped__' ? null : fields.farm, source: { kind: fields.source_kind, source_id: fields.source_id || null, reference: fields.source_reference }, rights_declaration: { owner: fields.owner || null, license_ref: fields.license_ref || null, permitted_uses: [...fields.uses], attribution: { required: fields.attribution_required, text: fields.attribution_text || null }, valid_from: fields.valid_from || null, expiry: fields.expiry || null }, sensitivity: fields.sensitivity, retention: { retain_until: fields.retain_until || null, legal_hold: fields.legal_hold } }
    let metadata: Metadata
    try { metadata = metadataFrom(value) } catch { throw Error('请检查所有必填字段、明确的 UTC/带时区时间和不含 URL 的来源引用。许可声明只能记录主张，不能自行批准。') }
    if (original) metadata = successorFrom(original, metadata)
    emit('prepare', metadata)
  } catch (failure) { error.value = failure instanceof Error ? failure.message : '输入不符合资产登记要求。' }
}
</script>
<template>
  <form data-testid="asset-metadata-editor" @submit.prevent="prepare" @input="dirty">
    <fieldset :disabled="disabled"><legend>{{ initial ? '追加不可变元数据版本' : '登记资产元数据（不是实体上传）' }}</legend>
      <div class="farm-fields">
        <label>资产名称<input v-model="fields.name" name="asset_name" required maxlength="200"></label>
        <label>资产类别<select v-model="fields.kind" name="asset_kind" required :disabled="!!initial"><option value="" disabled>明确选择类别</option><option v-for="kind in kinds" :key="kind" :value="kind">{{ kind }}</option></select></label>
        <label>数据等级<select v-model="fields.data_class" name="asset_data_class" required :disabled="!!initial"><option value="" disabled>明确选择等级</option><option value="demo">演示数据，不得混入正式处理</option><option value="operational">正式业务数据，仍需权利与实体验证</option></select></label>
        <label v-if="!initial">农场范围<select v-model="fields.farm" name="asset_farm" required><option value="" disabled>明确选择范围</option><option v-if="canUnscoped" value="__unscoped__">无所属农场（工作区治理登记）</option><option v-for="farm in farms" :key="farm.id" :value="farm.id">{{ farm.name }} · {{ farm.id }}</option></select></label>
        <p v-else>不可变农场范围：{{ initial.farm_id ?? '无所属农场' }}</p>
        <label>来源类别<select v-model="fields.source_kind" name="asset_source_kind" required @change="sourceChanged"><option value="" disabled>明确选择来源类别</option><option v-for="kind in sourceKinds" :key="kind" :value="kind">{{ kind }}</option></select></label>
        <label>来源引用（不是网址或存储 key）<input v-model="fields.source_reference" name="asset_source_reference" required maxlength="128"></label>
        <label>受审范围内来源登记<select v-model="fields.source_id" name="asset_source_id"><option value="">未识别来源，权利待审</option><option v-for="source in sources.filter(s => s.kind === fields.source_kind)" :key="source.source_id" :value="source.source_id">{{ source.label }} · {{ source.source_id }}</option></select></label>
        <label>敏感等级<select v-model="fields.sensitivity" name="asset_sensitivity" required><option value="" disabled>明确选择敏感等级</option><option v-for="value in sensitivities" :key="value" :value="value" :disabled="!!initial && sensitivities.indexOf(value) < sensitivities.indexOf(initial.sensitivity)">{{ value }}</option></select></label>
      </div>
      <fieldset><legend>未经批准的权利声明</legend>
        <p>填写来源或许可不代表审核通过。未知信息保留为空；没有可用权利的资产不能进入正式分析、训练、导出或共享。</p>
        <label>声明权利所有者（未知则留空）<input v-model="fields.owner" name="asset_rights_owner" maxlength="200"></label>
        <label>许可引用编号（不是 URL；未知则留空）<input v-model="fields.license_ref" name="asset_license_ref" maxlength="128"></label>
        <div data-testid="asset-declared-uses"><label v-for="purpose in purposes" :key="purpose" class="farm-checkbox"><input v-model="fields.uses" type="checkbox" :value="purpose" :name="`asset_use_${purpose}`">声明用途：{{ purpose }}</label></div>
        <label class="farm-checkbox"><input v-model="fields.attribution_required" type="checkbox" name="asset_attribution_required">声明需要署名</label>
        <label>署名文本<textarea v-model="fields.attribution_text" name="asset_attribution_text" maxlength="2048" rows="3"></textarea></label>
        <label>权利起始时间（含时区，未知则留空）<input v-model="fields.valid_from" name="asset_rights_from" placeholder="2026-11-01T00:00:00Z"></label>
        <label>权利到期时间（含时区，未知则留空）<input v-model="fields.expiry" name="asset_rights_expiry" placeholder="2027-11-01T00:00:00Z"></label>
      </fieldset>
      <fieldset><legend>保留与法律限制</legend><label>保留截止时间（留空表示持续保留，无截止期限）<input v-model="fields.retain_until" name="asset_retain_until" placeholder="2027-11-01T00:00:00Z" :readonly="!!initial && initial.retention.retain_until === null"></label><label class="farm-checkbox"><input v-model="fields.legal_hold" name="asset_legal_hold" type="checkbox" :disabled="!!initial?.retention.legal_hold">法律保留；后继版本不能在此解除</label></fieldset>
      <p>这里只登记元数据；不上传文件、不生成对象存储地址、不审批商业权利，也不把登记状态当成 ready。</p>
      <p v-if="error" role="alert" data-testid="asset-validation">{{ error }}</p>
      <button type="submit" data-testid="asset-preview">校验元数据并查看具体登记预览</button>
    </fieldset>
  </form>
</template>
