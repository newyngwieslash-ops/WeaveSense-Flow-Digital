import { expect, test } from 'bun:test'
import { boundaryDraft, createDraft, type Draft } from './draft'
const draft = (): Draft => ({ external_code: 'P1', name: 'Parcel', geometry: JSON.stringify({ type: 'Polygon', coordinates: [[[110, 25], [110.01, 25], [110.01, 25.01], [110, 25]]] }), source_crs: 'EPSG:4326', coordinate_order: 'longitude_latitude', reason: 'Explicit boundary record' })
test('parcel presentation draft preserves explicit CRS/axis/reason and does not calculate authority area or identity', () => {
  const input = draft(); const out = createDraft(input)
  expect(out.parcel.coordinate_order).toBe('longitude_latitude'); expect(out.reason).toBe(input.reason)
  expect(Object.keys(out.parcel).sort()).toEqual(['coordinate_order', 'external_code', 'geometry', 'name', 'source_crs'])
  expect(input).toEqual(draft()); expect(Object.keys(boundaryDraft(input)).sort()).toEqual(['coordinate_order', 'geometry', 'reason', 'source_crs'])
})
test('missing CRS, unknown axis, empty reason and malformed geometry do not turn into default data', () => {
  for (const override of [{ source_crs: '' }, { source_crs: 'EPSG:3857' }, { coordinate_order: 'latitude_longitude' }, { reason: ' ' }, { reason: 'x'.repeat(501) }, { geometry: '{' }, { geometry: '{"type":"Polygon","coordinates":[]}' }]) expect(() => boundaryDraft({ ...draft(), ...override })).toThrow()
  for (const override of [{ name: '' }, { external_code: '../P' }, { name: 'x'.repeat(201) }]) expect(() => createDraft({ ...draft(), ...override })).toThrow()
})
