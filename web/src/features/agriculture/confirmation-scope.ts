// Only confirmations owned by one protected page; never global destroyAll.
export class ConfirmationScope {
    private readonly handles = new Set<() => void>();
    add(destroy: () => void) {
        this.handles.add(destroy);
        return () => { this.handles.delete(destroy); };
    }
    clear() {
        const owned = [...this.handles];
        this.handles.clear();
        for (const destroy of owned) destroy();
    }
}
