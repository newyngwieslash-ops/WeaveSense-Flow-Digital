import { geometryText } from '../farms/geometry'
import type { Boundary, ParcelInput } from './types'
export type Draft = { external_code: string; name: string; geometry: string; source_crs: string; coordinate_order: string; reason: string }
export function boundaryDraft(draft: Draft): Boundary & { reason: string } {
  if (!['EPSG:4326', 'OGC:CRS84'].includes(draft.source_crs) || draft.coordinate_order !== 'longitude_latitude') throw new Error('请明确声明 CRS 与经度、纬度轴序；不会猜测或自动纠正。')
  if (!draft.reason.trim() || draft.reason.length > 500) throw new Error('请填写 1–500 字的创建或边界变更原因。')
  return { geometry: geometryText(draft.geometry), source_crs: draft.source_crs as Boundary['source_crs'], coordinate_order: 'longitude_latitude', reason: draft.reason }
}
export function createDraft(draft: Draft): { parcel: ParcelInput; reason: string } {
  if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(draft.external_code) || !draft.name.trim() || draft.name.length > 200) throw new Error('请填写有效业务编号和 1–200 字的地块名称。')
  const { reason, ...boundary } = boundaryDraft(draft)
  return { parcel: { external_code: draft.external_code, name: draft.name, ...boundary }, reason }
}
