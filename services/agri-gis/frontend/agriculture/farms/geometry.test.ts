import { describe, expect, test } from 'bun:test'
import { geometryText, parseGeometry } from './geometry'
const polygon = { type: 'Polygon', coordinates: [[[112, 25], [112.01, 25], [112.01, 25.01], [112, 25]]] }
describe('farm geometry display validation, not authoritative area', () => {
  test('accepts explicit lonlat rings without mutation and multipolygon', () => {
    const result = parseGeometry(polygon)
    expect(result).toEqual(polygon)
    expect(result).not.toBe(polygon)
    expect(parseGeometry({ type: 'MultiPolygon', coordinates: [polygon.coordinates] }).type).toBe('MultiPolygon')
  })
  test('rejects malformed and unclosed rings, bad axes, empty and foreign fields', () => {
    for (const value of [null, {}, [], { type: 'Point', coordinates: [0, 0] }, { ...polygon, crs: 'guess' }, { type: 'Polygon', coordinates: [] }, { type: 'Polygon', coordinates: [[[112, 25], [112, 26], [113, 26], [114, 25]]] }, { type: 'Polygon', coordinates: [[[25, 112], [26, 112], [26, 113], [25, 112]]] }, { type: 'Polygon', coordinates: [[[NaN, 25], [112, 26], [113, 26], [NaN, 25]]] }]) expect(() => parseGeometry(value)).toThrow()
    expect(() => geometryText('not JSON')).toThrow()
  })
  test('does not claim topology or area correctness', () => {
    // Self-intersection is deliberately left to authoritative server validation.
    expect(parseGeometry({ type: 'Polygon', coordinates: [[[0, 0], [1, 1], [0, 1], [1, 0], [0, 0]]] }).type).toBe('Polygon')
  })
})
