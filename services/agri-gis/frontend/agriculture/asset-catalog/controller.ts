import { FarmError } from '../farms/transport'
import { AssetAPI } from './api'
import type { Asset, Context, Intent, Operation, Page, Preview } from './types'
type API = Pick<AssetAPI, 'context' | 'list' | 'detail' | 'current' | 'preview' | 'submit' | 'operation'>
export type SafeReceipt = Pick<Operation, 'operation_id' | 'state' | 'status_url'>
export type State = { context: Context | null; page: Page; selected: Asset | null; comparison: Asset | null; preview: Preview | null; intent: Intent | null; operation: Operation | null; safeReceipt: SafeReceipt | null; busy: boolean; checking: boolean; uncertain: boolean; conflict: boolean; error: FarmError | null; editorEpoch: number; kind: string; dataClass: string }
const initial = (): State => ({ context: null, page: { items: [], next_cursor: '', has_more: false }, selected: null, comparison: null, preview: null, intent: null, operation: null, safeReceipt: null, busy: false, checking: false, uncertain: false, conflict: false, error: null, editorEpoch: 0, kind: '', dataClass: '' })
export class AssetController {
  state = initial(); onChange: (state: State) => void = () => {}
  private active: AbortController | null = null; private generation = 0; private disposed = false; private key = ''; private submitted = false
  constructor(readonly workspace: string, private readonly api: API = new AssetAPI(), private readonly authorityLost = () => {}, private readonly makeKey = () => crypto.randomUUID()) {}
  private publish(value: Partial<State>) { this.state = { ...this.state, ...value }; this.onChange(this.state) }
  get locked() { return this.disposed || this.state.busy || this.state.checking }
  get unresolved() { return this.state.uncertain || ['pending', 'reconciliation_required'].includes(this.state.operation?.state ?? '') || (this.submitted && this.state.operation?.state === 'preview') }
  setChecking(checking: boolean) { this.publish({ checking }) }
  private async run(work: (signal: AbortSignal) => Promise<Partial<State>>, writing = false) {
    if (this.locked) return
    this.active?.abort(); const active = new AbortController(); this.active = active; const generation = ++this.generation
    this.publish({ busy: true, error: null }); let timer: ReturnType<typeof setTimeout> | undefined
    try {
      const timeout = new Promise<never>((_r, reject) => { timer = setTimeout(() => { active.abort(); reject(new FarmError(0, 'TIMEOUT', '请求超时；元数据操作未确认，请查询原回执。')) }, 20000) })
      const next = await Promise.race([work(active.signal), timeout])
      if (!this.disposed && generation === this.generation && !active.signal.aborted) this.publish(next)
    } catch (error) {
      if (this.disposed || generation !== this.generation) return
      const failure = error instanceof FarmError ? error : new FarmError(0, 'NETWORK_ERROR', '资产请求结果未确认；保留草稿，不创建替代操作。')
      if ([401, 403, 404].includes(failure.status)) {
        const completed = !writing && failure.status !== 401 && this.state.operation?.state === 'completed' ? this.state.operation : null
        this.key = ''; this.submitted = false; this.publish({ ...initial(), busy: true, error: failure })
        if (completed) {
          // Content permission may expire after a version increment. Only a
          // fresh owner/membership-checked operation read may retain a receipt.
          clearTimeout(timer); let receiptTimer: ReturnType<typeof setTimeout> | undefined
          try {
            const deadline = new Promise<never>((_r, reject) => { receiptTimer = setTimeout(() => { active.abort(); reject(new Error('receipt deadline')) }, 5000) })
            const verified = await Promise.race([this.api.operation(this.workspace, completed.operation_id, active.signal), deadline])
            if (this.disposed || generation !== this.generation || active.signal.aborted) return
            if (verified.operation_id !== completed.operation_id || verified.state !== 'completed') throw Error('receipt no longer valid')
            this.publish({ safeReceipt: { operation_id: verified.operation_id, state: verified.state, status_url: verified.status_url } })
          } catch {
            if (!this.disposed && generation === this.generation) { this.publish({ safeReceipt: null }); this.authorityLost() }
          } finally { clearTimeout(receiptTimer) }
        } else if (failure.status !== 404) this.authorityLost()
      }
      else this.publish({ error: failure, conflict: failure.status === 409, uncertain: writing && (failure.status === 0 || failure.status >= 500) ? true : this.state.uncertain })
    } finally { clearTimeout(timer); if (!this.disposed && generation === this.generation) this.publish({ busy: false }) }
  }
  async initialize() { await this.run(async signal => { const [context, page] = await Promise.all([this.api.context(this.workspace, signal), this.api.list(this.workspace, signal)]); return { context, page, safeReceipt: null } }) }
  async list(kind = '', dataClass = '', more = false) {
    if (this.unresolved) return
    await this.run(async signal => { const page = await this.api.list(this.workspace, signal, kind, dataClass, more ? this.state.page.next_cursor : ''); return { kind, dataClass, page: more ? { ...page, items: [...this.state.page.items, ...page.items.filter(row => !this.state.page.items.some(old => old.asset_id === row.asset_id))] } : page } })
  }
  async select(id: string, version: number) {
    if (this.locked || this.unresolved) return
    await this.run(async signal => ({ selected: await this.api.detail(this.workspace, id, version, signal), comparison: null, intent: null, preview: null, operation: null, conflict: false, editorEpoch: this.state.editorEpoch + 1 }))
    this.key = ''; this.submitted = false
  }
  create() {
    if (this.locked || this.unresolved || !this.state.context || (!this.state.context.can_register_without_farm && !this.state.context.farms.length)) return
    this.key = ''; this.submitted = false; this.publish({ selected: null, comparison: null, intent: null, preview: null, operation: null, error: null, conflict: false, editorEpoch: this.state.editorEpoch + 1 })
  }
  draftChanged() { if (this.locked || this.unresolved) return; this.key = ''; this.submitted = false; this.publish({ preview: null, intent: null, operation: null, conflict: false, error: null }) }
  async prepare(intent: Intent) {
    if (this.locked || this.unresolved || !this.state.context) return
    if (intent.action === 'version' && (!this.state.selected || this.state.selected.asset_id !== intent.asset_id || this.state.selected.version !== this.state.selected.revision || intent.expected_revision !== this.state.selected.revision || !this.state.selected.allowed_actions.includes('agri.asset.create'))) return
    if (intent.action === 'create') {
      const context = this.state.context
      if (intent.metadata.farm_id === null ? !context.can_register_without_farm : !context.farms.some(f => f.farm_id === intent.metadata.farm_id)) return
      if (!intent.initial_grants.length || intent.initial_grants.some(g => !context.eligible_grantees.some(m => m.user_id === g.subject_user_id && g.capabilities.length > 0 && g.capabilities.every(c => m.allowed_capabilities.includes(c))))) return
    }
    const frozen = structuredClone(intent); let prepared: Preview | null = null
    await this.run(async signal => { prepared = await this.api.preview(this.workspace, frozen, signal); return { preview: prepared, intent: frozen, operation: null, comparison: null, conflict: false } })
    if (prepared && this.state.preview === prepared && !this.disposed) { this.key = this.makeKey(); this.submitted = false }
  }
  async confirm() {
    if (this.locked) return
    const { preview, intent } = this.state
    if (!preview || !intent || !this.key || this.state.operation?.state === 'completed') return
    if (!this.submitted && Date.parse(preview.expires_at) <= Date.now()) { this.publish({ preview: null, error: new FarmError(0, 'PREVIEW_EXPIRED', '预览已过期，请重新读取范围并生成新的具体预览。') }); return }
    await this.run(async signal => { this.submitted = true; return { operation: await this.api.submit(this.workspace, intent, preview, this.key, signal), uncertain: false, conflict: false } }, true)
  }
  async poll() { const id = this.state.operation?.operation_id ?? this.state.preview?.preview_id; if (id) await this.run(async signal => ({ operation: await this.api.operation(this.workspace, id, signal), uncertain: false })) }
  async lookup(id: string) { if (this.locked || this.unresolved || !/^aop_[A-Za-z0-9_-]{1,120}$/.test(id)) return; await this.run(async signal => ({ operation: await this.api.operation(this.workspace, id, signal), preview: null, intent: null, uncertain: false })); this.key = ''; this.submitted = false }
  async compare() { const selected = this.state.selected; if (!selected || this.unresolved) return; await this.run(async signal => ({ comparison: await this.api.current(this.workspace, selected.asset_id, selected.version, signal) })) }
  adoptComparison() { if (this.locked || this.unresolved || !this.state.comparison) return; this.key = ''; this.submitted = false; this.publish({ selected: this.state.comparison, comparison: null, preview: null, intent: null, operation: null, conflict: false, error: null, editorEpoch: this.state.editorEpoch + 1 }) }
  async refreshContext() { if (this.locked || this.unresolved) return; await this.run(async signal => ({ context: await this.api.context(this.workspace, signal), intent: null, preview: null, operation: null, conflict: false })); this.key = ''; this.submitted = false }
  dispose() { this.disposed = true; ++this.generation; this.active?.abort(); this.key = ''; this.submitted = false; this.publish(initial()) }
}
