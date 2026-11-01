<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import FarmBoundaryMap from '../farms/FarmBoundaryMap.vue'
import { geometryText } from '../farms/geometry'
import { boundaryDraft, createDraft, type Draft } from './draft'
import type { Intent, Parcel } from './types'
const props = defineProps<{ parcel: Parcel | null; farmID: string; farmRevision: number; disabled: boolean }>()
const emit = defineEmits<{ prepare: [intent: Intent]; dirty: [] }>()
const initial = props.parcel
const fields = reactive<Draft>({ external_code: initial?.external_code ?? '', name: initial?.name ?? '', geometry: initial ? JSON.stringify(initial.geometry, null, 2) : '', source_crs: initial?.source_crs ?? '', coordinate_order: initial?.coordinate_order ?? '', reason: '' })
const message = ref('')
const geometry = computed(() => { try { return geometryText(fields.geometry) } catch { return null } })
function dirty() { message.value = ''; emit('dirty') }
function prepare() {
  if (props.disabled) return
  try {
    if (initial) {
      if (initial.geometry_revision !== initial.current_geometry_revision) throw new Error('历史版本只读；请重新读取当前边界后编辑。')
      emit('prepare', { action: 'revise', parcel_id: initial.id, expected_revision: initial.geometry_revision, ...boundaryDraft(fields) })
    } else emit('prepare', { action: 'create', farm_id: props.farmID, expected_revision: 0, expected_farm_revision: props.farmRevision, ...createDraft(fields) })
  } catch (error) { message.value = error instanceof Error ? error.message : '输入无效。' }
}
</script>
<template>
  <form data-testid="parcel-editor" @submit.prevent="prepare" @input="dirty">
    <fieldset :disabled="disabled">
      <legend>{{ parcel ? `追加边界修订 · 预期版本 ${parcel.geometry_revision}` : `创建稳定地块 · 父农场版本 ${farmRevision}` }}</legend>
      <label>地块业务编号<input v-model="fields.external_code" name="parcel_external_code" required maxlength="64" :readonly="!!parcel"></label>
      <label>地块名称<input v-model="fields.name" name="parcel_name" required maxlength="200" :readonly="!!parcel"></label>
      <label>原始坐标系<select v-model="fields.source_crs" name="parcel_source_crs" required><option value="" disabled>明确声明 CRS</option><option value="EPSG:4326">EPSG:4326</option><option value="OGC:CRS84">OGC:CRS84</option></select></label>
      <label>接口坐标轴序<select v-model="fields.coordinate_order" name="parcel_coordinate_order" required><option value="" disabled>明确声明轴序</option><option value="longitude_latitude">经度、纬度</option></select></label>
      <label>GeoJSON geometry<textarea v-model="fields.geometry" name="parcel_geometry" required rows="8" spellcheck="false"></textarea></label>
      <label>创建或边界变更原因<textarea v-model="fields.reason" name="parcel_reason" required maxlength="500" rows="3"></textarea></label>
      <FarmBoundaryMap :geometry="geometry" />
      <p>仅接收 Polygon / MultiPolygon。拓扑、父农场包含关系与椭球面积由服务端校验；不自动修复或裁剪。有效时间由服务端确定，不回填历史。</p>
      <p>地块身份长期保留，修改只新增不可变边界版本；数据类别继承父农场，不能将演示数据转为正式数据。</p>
      <p v-if="message" role="alert" data-testid="parcel-validation">{{ message }}</p>
      <button type="submit" data-testid="parcel-preview">校验并预览具体边界变更</button>
    </fieldset>
  </form>
</template>
