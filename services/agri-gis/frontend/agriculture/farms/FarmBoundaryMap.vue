<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import Map from 'ol/Map'
import View from 'ol/View'
import GeoJSON from 'ol/format/GeoJSON'
import VectorLayer from 'ol/layer/Vector'
import VectorSource from 'ol/source/Vector'
import Draw from 'ol/interaction/Draw'
import { Fill, Stroke, Style } from 'ol/style'
import 'ol/ol.css'
import { parseGeometry, type FarmGeometry } from './geometry'

const props = defineProps<{ geometry: FarmGeometry | null; editable?: boolean; disabled?: boolean }>()
const emit = defineEmits<{ change: [geometry: FarmGeometry] }>()
const target = ref<HTMLDivElement>()
const message = ref('')
const drawing = ref(false)
const source = new VectorSource()
const format = new GeoJSON()
let map: Map | undefined
let draw: Draw | undefined
function stopDrawing() { if (draw) map?.removeInteraction(draw); draw = undefined; drawing.value = false }
function render() {
  source.clear(); message.value = ''
  if (!props.geometry || !map) return
  try {
    const geometry = parseGeometry(props.geometry)
    source.addFeatures(format.readFeatures({ type: 'Feature', geometry, properties: {} }, { dataProjection: 'EPSG:4326', featureProjection: 'EPSG:3857' }))
    const extent = source.getExtent()
    if (!extent || extent.some(value => !Number.isFinite(value))) throw new Error('invalid display extent')
    map.getView().fit(extent, { padding: [30, 30, 30, 30], maxZoom: 18, duration: 0 })
  } catch { source.clear(); message.value = '边界无法安全显示，请检查输入；尚未修改服务器数据。' }
}
function startDrawing() {
  if (!map || !props.editable || props.disabled) return
  stopDrawing(); drawing.value = true
  draw = new Draw({ type: 'Polygon' })
  draw.on('drawend', event => {
    try {
      const geometry = parseGeometry(format.writeGeometryObject(event.feature.getGeometry()!, { featureProjection: 'EPSG:3857', dataProjection: 'EPSG:4326', decimals: 8 }))
      emit('change', geometry)
    } catch { message.value = '绘制结果无效，请重新绘制。' }
    stopDrawing()
  })
  map.addInteraction(draw)
}
onMounted(() => {
  const layer = new VectorLayer({ source, style: new Style({ stroke: new Stroke({ color: '#176640', width: 2 }), fill: new Fill({ color: 'rgba(29,127,80,0.18)' }) }) })
  map = new Map({ target: target.value, layers: [layer], view: new View({ center: [0, 0], zoom: 1, multiWorld: false }) })
  render()
})
watch(() => props.geometry, render, { deep: true })
watch(() => props.disabled, value => { if (value) stopDrawing() })
onUnmounted(() => { stopDrawing(); map?.setTarget(undefined); map?.dispose() })
</script>
<template>
  <div class="farm-map-container">
    <div ref="target" class="farm-map" data-testid="farm-map" role="img" aria-label="农场边界二维预览，无外部底图"></div>
    <p class="muted">仅显示边界，无外部底图。显示投影 EPSG:3857；提交仍为 WGS84 经度/纬度。面积以服务器结果为准。</p>
    <button v-if="editable" type="button" :disabled="disabled" @click="drawing ? stopDrawing() : startDrawing()">{{ drawing ? '取消绘制' : '绘制替换边界' }}</button>
    <p v-if="drawing" role="status">点击添加顶点，双击完成；只更新草稿，仍需预览并确认提交。</p>
    <p v-if="message" role="alert">{{ message }}</p>
  </div>
</template>
