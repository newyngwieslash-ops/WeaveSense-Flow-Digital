import { FarmError } from '../farms/transport'
import { ParcelAPI } from './api'
import type { HistoryPage, Intent, Operation, Parcel, ParcelContext, ParcelPage, Preview, Selector } from './types'
type API = Pick<ParcelAPI, 'context' | 'list' | 'detail' | 'history' | 'preview' | 'submit' | 'operation'>
export type ParcelState = { context: ParcelContext | null; page: ParcelPage; selected: Parcel | null; comparison: Parcel | null; history: HistoryPage; preview: Preview | null; intent: Intent | null; operation: Operation | null; busy: boolean; checking: boolean; uncertain: boolean; conflict: boolean; error: FarmError | null; editorEpoch: number }
const initial = (): ParcelState => ({ context: null, page: { items: [], next_cursor: '', has_more: false }, selected: null, comparison: null, history: { items: [], next_cursor: '', has_more: false }, preview: null, intent: null, operation: null, busy: false, checking: false, uncertain: false, conflict: false, error: null, editorEpoch: 0 })
export class ParcelController {
  state = initial()
  onChange: (state: ParcelState) => void = () => {}
  private active: AbortController | null = null
  private generation = 0
  private disposed = false
  private key = ''
  private submitted = false
  constructor(readonly workspace: string, readonly parent: string, private readonly api: API = new ParcelAPI(), private readonly authorityLost = () => {}, private readonly makeKey = () => crypto.randomUUID()) {}
  private publish(next: Partial<ParcelState>) { this.state = { ...this.state, ...next }; this.onChange(this.state) }
  get locked() { return this.state.busy || this.state.checking || this.disposed }
  get unresolved() { return this.state.uncertain || ['pending', 'reconciliation_required'].includes(this.state.operation?.state ?? '') || (this.submitted && this.state.operation?.state === 'preview') }
  setChecking(checking: boolean) { this.publish({ checking }) }
  private async run(work: (signal: AbortSignal) => Promise<Partial<ParcelState>>, writing = false) {
    if (this.locked) return
    this.active?.abort(); const active = new AbortController(); this.active = active; const generation = ++this.generation
    this.publish({ busy: true, error: null })
    let timer: ReturnType<typeof setTimeout> | undefined
    try {
      const deadline = new Promise<never>((_r, reject) => { timer = setTimeout(() => { active.abort(); reject(new FarmError(0, 'TIMEOUT', '请求超时；结果未确认，请查询原地块操作。')) }, 20000) })
      const next = await Promise.race([work(active.signal), deadline])
      if (generation === this.generation && !this.disposed && !active.signal.aborted) this.publish(next)
    } catch (error) {
      if (generation !== this.generation || this.disposed) return
      const failure = error instanceof FarmError ? error : new FarmError(0, 'NETWORK_ERROR', '网络请求未确认；保留草稿，不能另建操作掩盖未知结果。')
      if ([401, 403, 404].includes(failure.status)) { this.key = ''; this.submitted = false; this.publish({ ...initial(), error: failure }); if (failure.status !== 404) this.authorityLost() }
      else this.publish({ error: failure, conflict: failure.status === 409, uncertain: writing && (failure.status === 0 || failure.status >= 500) ? true : this.state.uncertain })
    } finally { clearTimeout(timer); if (generation === this.generation && !this.disposed) this.publish({ busy: false }) }
  }
  async select(id: string, selector?: Selector) {
    if (this.locked || this.unresolved) return
    await this.run(async signal => ({ selected: await this.api.detail(this.workspace, this.parent, id, signal, selector), comparison: null, history: { items: [], next_cursor: '', has_more: false }, preview: null, intent: null, operation: null, conflict: false, editorEpoch: this.state.editorEpoch + 1 }))
    this.key = ''; this.submitted = false
  }
  async initialize() {
    await this.run(async signal => {
      const context = await this.api.context(this.workspace, this.parent, signal)
      signal.throwIfAborted()
      const page = context.allowed_actions.includes('agri.parcel.read') ? await this.api.list(this.workspace, this.parent, signal) : { items: [], next_cursor: '', has_more: false }
      return { context, page }
    })
  }
  async list(more = false) {
    if (this.unresolved || !this.state.context?.allowed_actions.includes('agri.parcel.read')) return
    await this.run(async signal => {
      const page = await this.api.list(this.workspace, this.parent, signal, more ? this.state.page.next_cursor : '')
      return { page: more ? { ...page, items: [...this.state.page.items, ...page.items.filter(row => !this.state.page.items.some(old => old.id === row.id))] } : page }
    })
  }
  async refreshContext() {
    if (this.locked || this.unresolved) return
    await this.run(async signal => ({ context: await this.api.context(this.workspace, this.parent, signal), preview: null, intent: null, operation: null, conflict: false }))
    this.key = ''; this.submitted = false
  }
  async history(more = false) {
    const selected = this.state.selected
    if (!selected || this.unresolved) return
    await this.run(async signal => {
      const page = await this.api.history(this.workspace, this.parent, selected.id, signal, more ? this.state.history.next_cursor : '')
      return { history: more ? { ...page, items: [...this.state.history.items, ...page.items.filter(row => !this.state.history.items.some(old => old.geometry_revision === row.geometry_revision))] } : page }
    })
  }
  create() {
    if (this.locked || this.unresolved || !this.state.context?.allowed_actions.includes('agri.parcel.create')) return
    this.key = ''; this.submitted = false
    this.publish({ selected: null, comparison: null, history: { items: [], next_cursor: '', has_more: false }, preview: null, intent: null, operation: null, error: null, conflict: false, editorEpoch: this.state.editorEpoch + 1 })
  }
  draftChanged() {
    if (this.locked || this.unresolved) return
    this.key = ''; this.submitted = false; this.publish({ preview: null, intent: null, operation: null, error: null, conflict: false })
  }
  async prepare(intent: Intent) {
    if (this.locked || this.unresolved) return
    if (intent.action === 'create' && (!this.state.context?.allowed_actions.includes('agri.parcel.create') || intent.farm_id !== this.parent || intent.expected_revision !== 0 || intent.expected_farm_revision !== this.state.context.expected_farm_revision)) return
    if (intent.action === 'revise' && (!this.state.selected || this.state.selected.id !== intent.parcel_id || this.state.selected.geometry_revision !== this.state.selected.current_geometry_revision || intent.expected_revision !== this.state.selected.geometry_revision || !this.state.selected.allowed_actions.includes('agri.parcel.update'))) return
    const frozen = structuredClone(intent); let prepared: Preview | null = null
    await this.run(async signal => { prepared = await this.api.preview(this.workspace, this.parent, frozen, signal); return { preview: prepared, intent: frozen, operation: null, comparison: null, conflict: false } })
    if (prepared && this.state.preview === prepared && !this.disposed) { this.key = this.makeKey(); this.submitted = false }
  }
  async confirm() {
    if (this.locked) return
    const { preview, intent } = this.state
    if (!preview || !intent || !this.key || this.state.operation?.state === 'completed') return
    if (!this.submitted && Date.parse(preview.expires_at) <= Date.now()) { this.publish({ preview: null, error: new FarmError(0, 'PREVIEW_EXPIRED', '预览已过期，请重新校验边界并确认。') }); return }
    await this.run(async signal => { this.submitted = true; return { operation: await this.api.submit(this.workspace, this.parent, intent, preview, this.key, signal), uncertain: false, conflict: false } }, true)
  }
  async poll() {
    const id = this.state.operation?.operation_id ?? this.state.preview?.preview_id
    if (id) await this.run(async signal => ({ operation: await this.api.operation(this.workspace, this.parent, id, signal), uncertain: false }))
  }
  async lookup(id: string) {
    if (this.locked || this.unresolved || !/^pop_[A-Za-z0-9_-]{1,120}$/.test(id)) return
    await this.run(async signal => ({ operation: await this.api.operation(this.workspace, this.parent, id, signal), preview: null, intent: null, uncertain: false }))
    this.key = ''; this.submitted = false
  }
  async compare() {
    const selected = this.state.selected
    if (!selected || this.unresolved) return
    await this.run(async signal => ({ comparison: await this.api.detail(this.workspace, this.parent, selected.id, signal) }))
  }
  adoptComparison() {
    if (this.locked || this.unresolved || !this.state.comparison) return
    this.key = ''; this.submitted = false
    this.publish({ selected: this.state.comparison, comparison: null, preview: null, intent: null, operation: null, conflict: false, error: null, editorEpoch: this.state.editorEpoch + 1 })
  }
  dispose() { this.disposed = true; ++this.generation; this.active?.abort(); this.key = ''; this.submitted = false; this.publish(initial()) }
}
