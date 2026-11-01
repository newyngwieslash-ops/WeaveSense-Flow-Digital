// Presentation validation only. PostGIS owns topology and authoritative area.
export type Position = [number, number]
export type PolygonGeometry = { type: 'Polygon'; coordinates: Position[][] }
export type MultiPolygonGeometry = { type: 'MultiPolygon'; coordinates: Position[][][] }
export type FarmGeometry = PolygonGeometry | MultiPolygonGeometry
export function parseGeometry(value: unknown): FarmGeometry {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('边界必须是 GeoJSON Polygon 或 MultiPolygon。')
  const geometry = value as Record<string, unknown>
  if (!['Polygon', 'MultiPolygon'].includes(String(geometry.type)) || Object.keys(geometry).some(key => !['type', 'coordinates'].includes(key))) throw new Error('仅接受 Polygon 或 MultiPolygon geometry，不接受 Feature 或未知字段。')
  const polygons = geometry.type === 'Polygon' ? [geometry.coordinates] : geometry.coordinates
  let points = 0
  if (!Array.isArray(polygons) || !polygons.length) throw new Error('边界不能为空。')
  for (const polygon of polygons) {
    if (!Array.isArray(polygon) || !polygon.length) throw new Error('多边形不能为空。')
    for (const ring of polygon) {
      if (!Array.isArray(ring) || ring.length < 4) throw new Error('每个环至少包含四个点并闭合。')
      for (const position of ring) {
        if (!Array.isArray(position) || position.length !== 2 || position.some(v => typeof v !== 'number' || !Number.isFinite(v)) || Math.abs(position[0]) > 180 || Math.abs(position[1]) > 90) throw new Error('坐标必须是有限的 [经度, 纬度]，不得自动猜测或纠正轴序。')
        if (++points > 10000) throw new Error('交互编辑最多支持 10000 个坐标点。')
      }
      if (ring[0][0] !== ring[ring.length - 1][0] || ring[0][1] !== ring[ring.length - 1][1]) throw new Error('边界环未闭合，请明确补齐终点。')
    }
  }
  return structuredClone(geometry) as FarmGeometry
}
export function geometryText(value: string): FarmGeometry {
  let parsed: unknown
  try { parsed = JSON.parse(value) } catch { throw new Error('边界不是有效 JSON。') }
  return parseGeometry(parsed)
}
