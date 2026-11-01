<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import FarmBoundaryMap from './FarmBoundaryMap.vue'
import { geometryText, type FarmGeometry } from './geometry'
import type { AreaUnit, Capability, Farm, FarmContext, FarmInput, InitialGrant, Intent } from './types'
const props = defineProps<{ farm: Farm | null; context: FarmContext; disabled: boolean }>()
const emit = defineEmits<{ prepare: [intent: Intent]; dirty: [] }>()
const initial = props.farm
const fields = reactive({ external_code: initial?.external_code ?? '', name: initial?.name ?? '', operator_name: initial?.operator_name ?? '', contact_name: initial?.contact?.name ?? '', email: initial?.contact?.email ?? '', phone: initial?.contact?.phone ?? '', region_code: initial?.region_code ?? '', timezone: initial?.timezone ?? '', area_unit: initial?.area_unit ?? '', source_crs: initial?.source_crs ?? '', data_class: initial?.data_class ?? '', geometry: initial ? JSON.stringify(initial.geometry, null, 2) : '' })
const grants = reactive<Record<string, { capabilities: Capability[]; expires: string }>>(Object.fromEntries(props.context.members.map(member => [member.user_id, { capabilities: [], expires: '' }])))
const message = ref('')
const geometry = computed(() => { try { return geometryText(fields.geometry) } catch { return null } })
const timezones = computed(() => props.context.regions.find(region => region.code === fields.region_code)?.timezones ?? [])
function dirty() { message.value = ''; emit('dirty') }
function regionChanged() { if (!timezones.value.includes(fields.timezone)) fields.timezone = ''; dirty() }
function drawn(value: FarmGeometry) { fields.geometry = JSON.stringify(value, null, 2); fields.source_crs = 'EPSG:4326'; dirty() }
function prepare() {
  if (props.disabled) return
  try {
    if (!props.context.regions.some(region => region.code === fields.region_code) || !timezones.value.includes(fields.timezone) || !props.context.area_units.includes(fields.area_unit as AreaUnit) || !['EPSG:4326', 'OGC:CRS84'].includes(fields.source_crs) || !['demo', 'operational'].includes(fields.data_class)) throw new Error('请明确选择区域、时区、单位、原始坐标系和数据类别。')
    if (!fields.name.trim() || !fields.operator_name.trim() || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(fields.external_code)) throw new Error('请填写名称、经营主体和有效业务编号。')
    const farm: FarmInput = { external_code: fields.external_code, name: fields.name, operator_name: fields.operator_name, contact: { name: fields.contact_name, email: fields.email, phone: fields.phone }, region_code: fields.region_code, dictionary_version: props.context.dictionary_version, timezone: fields.timezone, area_unit: fields.area_unit as AreaUnit, geometry: geometryText(fields.geometry), source_crs: fields.source_crs as FarmInput['source_crs'], coordinate_order: 'longitude_latitude', data_class: fields.data_class as FarmInput['data_class'] }
    if (initial) {
      const { external_code: _external, data_class: _class, ...changes } = farm
      emit('prepare', { action: 'update', farm_id: initial.id, expected_revision: initial.revision, farm: changes })
    } else {
      const initial_grants: InitialGrant[] = props.context.members.filter(m => grants[m.user_id]?.capabilities.length).map(member => {
        const choice = grants[member.user_id]!
        if (choice.capabilities.some(capability => !member.allowed_capabilities.includes(capability))) throw new Error('成员权限上限已变化，请重新读取工作区配置。')
        const grant: InitialGrant = { subject_user_id: member.user_id, capabilities: [...choice.capabilities] }
        if (choice.expires) {
          const expiry = new Date(choice.expires)
          if (!Number.isFinite(expiry.getTime()) || expiry.getTime() <= Date.now()) throw new Error('授权有效期必须是明确的未来时间。')
          grant.expires_at = expiry.toISOString()
        }
        return grant
      })
      if (!initial_grants.length || initial_grants.length > 20) throw new Error('请显式选择 1–20 位已有业务角色成员的初始授权；不会自动授权创建人。')
      emit('prepare', { action: 'create', farm, initial_grants })
    }
  } catch (error) { message.value = error instanceof Error ? error.message : '输入无效。' }
}
</script>
<template>
  <form data-testid="farm-editor" @submit.prevent="prepare" @input="dirty">
    <fieldset :disabled="disabled">
      <legend>{{ farm ? `编辑农场 · 当前读取版本 ${farm.revision}` : '创建农场 · 明确地域与初始授权' }}</legend>
      <div class="farm-fields">
        <label>业务编号<input v-model="fields.external_code" name="external_code" required maxlength="64" pattern="[A-Za-z0-9][A-Za-z0-9_-]{0,63}" :readonly="!!farm"></label>
        <label>农场名称<input v-model="fields.name" name="name" required maxlength="200"></label>
        <label>经营主体<input v-model="fields.operator_name" name="operator_name" required maxlength="200"></label>
        <label>联系人<input v-model="fields.contact_name" name="contact_name" maxlength="120" autocomplete="off"></label>
        <label>联系邮箱<input v-model="fields.email" name="email" type="email" maxlength="254" autocomplete="off"></label>
        <label>联系电话<input v-model="fields.phone" name="phone" type="tel" maxlength="40" autocomplete="off"></label>
        <label>经营区域<select v-model="fields.region_code" name="region_code" required @change="regionChanged"><option value="" disabled>明确选择区域</option><option v-for="region in context.regions" :key="region.code" :value="region.code">{{ region.name }} · {{ region.code }}</option></select></label>
        <label>业务时区<select v-model="fields.timezone" name="timezone" required><option value="" disabled>明确选择时区</option><option v-for="zone in timezones" :key="zone" :value="zone">{{ zone }}</option></select></label>
        <label>面积显示单位<select v-model="fields.area_unit" name="area_unit" required><option value="" disabled>明确选择单位</option><option v-for="unit in context.area_units" :key="unit" :value="unit">{{ ({ m2: '平方米', ha: '公顷', mu: '亩' })[unit] }}</option></select></label>
        <label>数据类别<select v-model="fields.data_class" name="data_class" required :disabled="!!farm"><option value="" disabled>明确选择数据类别</option><option value="demo">演示数据 · 不得作为正式生产资料</option><option value="operational">正式业务数据</option></select></label>
        <label>原始坐标系<select v-model="fields.source_crs" name="source_crs" required><option value="" disabled>明确声明原始 CRS</option><option value="EPSG:4326">EPSG:4326（接口使用经度、纬度）</option><option value="OGC:CRS84">OGC:CRS84（经度、纬度）</option></select></label>
      </div>
      <p class="muted">区域目录版本 {{ context.dictionary_version }}。目录选项不等于行政覆盖已核实。编号与数据类别创建后不可更改。</p>
      <label>边界 GeoJSON geometry（Polygon / MultiPolygon）<textarea v-model="fields.geometry" name="geometry" required rows="8" spellcheck="false" aria-describedby="farm-crs-note"></textarea></label>
      <p id="farm-crs-note" class="muted">坐标必须为 [经度, 纬度]。不猜测 CRS，不自动修复拓扑。此版本不接受其他 CRS；服务端校验并计算椭球面积。</p>
      <FarmBoundaryMap :geometry="geometry" editable :disabled="disabled" @change="drawn" />
      <fieldset v-if="!farm" class="farm-grants"><legend>初始明确授权（不会自动给所有者或创建人业务权限）</legend>
        <p v-if="!context.members.some(member => member.allowed_capabilities.length)" role="status">尚无合适业务角色的成员。请先在平台组织页面配置成员角色，再刷新。</p>
        <div v-for="member in context.members.filter(member => member.allowed_capabilities.length)" :key="member.user_id" class="farm-grant">
          <strong>{{ member.display_name }}</strong><code>{{ member.user_id }}</code>
          <label v-for="capability in member.allowed_capabilities" :key="capability" class="farm-checkbox"><input v-model="grants[member.user_id]!.capabilities" type="checkbox" :value="capability">{{ capability }}</label>
          <label>授权到期时间（可选，浏览器本地时间）<input v-model="grants[member.user_id]!.expires" type="datetime-local"></label>
        </div>
      </fieldset>
      <p v-if="message" role="alert" data-testid="farm-validation">{{ message }}</p>
      <button type="submit" data-testid="farm-preview">校验并生成具体操作预览</button>
    </fieldset>
  </form>
</template>
