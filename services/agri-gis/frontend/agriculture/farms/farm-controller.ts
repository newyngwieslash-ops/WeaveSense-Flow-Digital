import { FarmAPI } from './api'
import { FarmError } from './transport'
import type { Farm, FarmContext, FarmPage, Intent, Operation, Preview } from './types'
type API = Pick<FarmAPI, 'context' | 'list' | 'detail' | 'preview' | 'submit' | 'operation'>
export type FarmState = {
  context: FarmContext | null; page: FarmPage; selected: Farm | null; comparison: Farm | null
  preview: Preview | null; intent: Intent | null; operation: Operation | null
  busy: boolean; checking: boolean; error: FarmError | null; uncertain: boolean; conflict: boolean
  status: 'active' | 'archived'; search: string; editorEpoch: number
}
const initial = (): FarmState => ({ context: null, page: { items: [], next_cursor: '', has_more: false }, selected: null, comparison: null, preview: null, intent: null, operation: null, busy: false, checking: false, error: null, uncertain: false, conflict: false, status: 'active', search: '', editorEpoch: 0 })
export class FarmController {
  state = initial()
  onChange: (state: FarmState) => void = () => {}
  private active: AbortController | null = null
  private generation = 0
  private key = ''
  private submitted = false
  private disposed = false
  constructor(readonly workspace: string, private readonly api: API = new FarmAPI(), private readonly authorityLost = () => {}, private readonly makeKey = () => crypto.randomUUID()) {}
  private publish(next: Partial<FarmState>) { this.state = { ...this.state, ...next }; this.onChange(this.state) }
  setChecking(checking: boolean) { this.publish({ checking }) }
  get locked() { return this.state.busy || this.state.checking || this.disposed }
  get unresolved() { return this.state.uncertain || ['pending', 'reconciliation_required'].includes(this.state.operation?.state ?? '') || (this.submitted && this.state.operation?.state === 'preview') }
  private async run(work: (signal: AbortSignal) => Promise<Partial<FarmState>>, writing = false) {
    if (this.locked) return
    this.active?.abort(); const active = new AbortController(); this.active = active; const generation = ++this.generation
    this.publish({ busy: true, error: null })
    let timeout: ReturnType<typeof setTimeout> | undefined
    try {
      const deadline = new Promise<never>((_resolve, reject) => {
        timeout = setTimeout(() => { active.abort(); reject(new FarmError(0, 'TIMEOUT', '请求超时；未确认结果，请查询原操作状态。')) }, 20000)
      })
      const next = await Promise.race([work(active.signal), deadline])
      if (this.generation === generation && !this.disposed && !active.signal.aborted) this.publish(next)
      else if (this.generation === generation && active.signal.aborted) throw new FarmError(0, 'TIMEOUT', '请求超时；未确认结果，请查询原操作状态。')
    } catch (error) {
      if (this.generation !== generation || this.disposed) return
      const failure = error instanceof FarmError ? error : new FarmError(0, 'NETWORK_ERROR', '网络请求未确认；草稿保留，请勿重复创建另一操作。')
      if ([401, 403, 404].includes(failure.status)) {
        this.publish({ ...initial(), error: failure }); this.key = ''; this.submitted = false
        // A missing resource is not proof that the session changed. Avoid a
        // mount -> 404 -> session refresh -> remount loop for unavailable APIs.
        if (failure.status !== 404) this.authorityLost()
      } else this.publish({ error: failure, conflict: failure.status === 409, uncertain: writing && (failure.status === 0 || failure.status >= 500) ? true : this.state.uncertain })
    } finally {
      clearTimeout(timeout)
      if (this.generation === generation && !this.disposed) this.publish({ busy: false })
    }
  }
  async initialize() {
    await this.run(async signal => {
      const [context, page] = await Promise.all([this.api.context(this.workspace, signal), this.api.list(this.workspace, signal)])
      return { context, page }
    })
  }
  async list(status: 'active' | 'archived', search: string, more = false) {
    if (this.unresolved) return
    await this.run(async signal => {
      const page = await this.api.list(this.workspace, signal, status, search, more ? this.state.page.next_cursor : '')
      return { page: more ? { ...page, items: [...this.state.page.items, ...page.items.filter(row => !this.state.page.items.some(old => old.id === row.id))] } : page, status, search }
    })
  }
  async select(id: string) {
    if (this.locked || this.unresolved) return
    await this.run(async signal => ({ selected: await this.api.detail(this.workspace, id, signal), comparison: null, preview: null, intent: null, operation: null, conflict: false, editorEpoch: this.state.editorEpoch + 1 }))
    this.key = ''; this.submitted = false
  }
  create() {
    if (this.locked || this.unresolved || !this.state.context?.can_create) return
    this.key = ''; this.submitted = false
    this.publish({ selected: null, comparison: null, preview: null, intent: null, operation: null, error: null, conflict: false, editorEpoch: this.state.editorEpoch + 1 })
  }
  draftChanged() {
    if (this.unresolved || this.state.busy) return
    this.key = ''; this.submitted = false
    this.publish({ preview: null, intent: null, operation: null, error: null, conflict: false })
  }
  async prepare(intent: Intent) {
    if (this.locked || this.unresolved) return
    const frozen = structuredClone(intent)
    let prepared: Preview | null = null
    await this.run(async signal => {
      const preview = await this.api.preview(this.workspace, frozen, signal)
      prepared = preview
      return { preview, intent: frozen, operation: null, conflict: false, comparison: null }
    })
    if (prepared && this.state.preview === prepared && !this.disposed) { this.key = this.makeKey(); this.submitted = false }
  }
  async confirm() {
    if (this.locked) return
    const { preview, intent } = this.state
    if (!preview || !intent || !this.key || this.state.operation?.state === 'completed') return
    if (!this.submitted && Date.parse(preview.expires_at) <= Date.now()) { this.publish({ error: new FarmError(0, 'PREVIEW_EXPIRED', '预览已过期，请重新生成并确认。'), preview: null }); return }
    await this.run(async signal => {
      this.submitted = true
      const operation = await this.api.submit(this.workspace, intent, preview, this.key, signal)
      return { operation, uncertain: false, conflict: false }
    }, true)
  }
  async poll() {
    const id = this.state.operation?.operation_id ?? this.state.preview?.preview_id
    if (!id) return
    await this.run(async signal => ({ operation: await this.api.operation(this.workspace, id, signal), uncertain: false }))
  }
  async lookup(id: string) {
    if (this.locked || this.unresolved || !/^fop_[A-Za-z0-9_-]{1,120}$/.test(id)) return
    await this.run(async signal => ({ operation: await this.api.operation(this.workspace, id, signal), preview: null, intent: null, uncertain: false }))
    this.key = ''; this.submitted = false
  }
  async compare() {
    if (!this.state.selected || this.unresolved) return
    const id = this.state.selected.id
    await this.run(async signal => ({ comparison: await this.api.detail(this.workspace, id, signal) }))
  }
  adoptComparison() {
    if (this.locked || this.unresolved || !this.state.comparison) return
    this.key = ''; this.submitted = false
    this.publish({ selected: this.state.comparison, comparison: null, preview: null, intent: null, operation: null, conflict: false, error: null, editorEpoch: this.state.editorEpoch + 1 })
  }
  dispose() { this.disposed = true; ++this.generation; this.active?.abort(); this.key = ''; this.submitted = false; this.publish(initial()) }
}
