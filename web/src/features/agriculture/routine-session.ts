import { useSyncExternalStore } from 'react';
import type { AgricultureWorkspace } from '@/services/api/agriculture';

let checking = false;
let pending = false, businessActivities = 0;
const idleListeners = new Set<() => void>();
const listeners = new Set<() => void>();
const participants = new Set<(signal: AbortSignal) => Promise<void>>();
export function needsSessionBootstrap(snapshot: string | null, trustedActor: string | undefined, currentActor: string | undefined): boolean {
    return snapshot === null || trustedActor !== currentActor;
}
let displayed: { actorId: string; workspaces: AgricultureWorkspace[] } | null = null;
let projectionGeneration = 0;
export const currentProjectionGeneration = () => projectionGeneration;
export function acceptDisplayedAuthority(actorId: string | undefined, workspaces: AgricultureWorkspace[], generation: number) {
    if (actorId && generation === projectionGeneration) displayed = { actorId, workspaces };
}
export const displayedAuthority = () => displayed;
export function clearDisplayedAuthority() { ++projectionGeneration; displayed = null; }
export function setRoutineChecking(value: boolean) {
    checking = value;
    if (!value) pending = false;
    for (const listener of listeners) listener();
}
export const isRoutineChecking = () => checking;
export function useRoutineChecking() {
    return useSyncExternalStore(callback => { listeners.add(callback); return () => { listeners.delete(callback); }; }, () => checking || pending);
}
export function acquireBusinessActivity(): (() => void) | null {
    if (checking || pending) return null;
    ++businessActivities;
    let released = false;
    return () => {
        if (released) return;
        released = true; --businessActivities;
        if (!businessActivities) for (const listener of [...idleListeners]) listener();
    };
}
export function waitForBusinessIdle(signal: AbortSignal): Promise<void> {
    pending = true;
    for (const listener of listeners) listener();
    return new Promise((resolve, reject) => {
        const cleanup = () => { idleListeners.delete(idle); signal.removeEventListener('abort', abort); };
        const idle = () => { if (!businessActivities) { cleanup(); resolve(); } };
        const abort = () => { cleanup(); reject(new DOMException('Obsolete check', 'AbortError')); };
        if (signal.aborted) { abort(); return; }
        idleListeners.add(idle); signal.addEventListener('abort', abort, { once: true }); idle();
    });
}
export function registerRoutineParticipant(check: (signal: AbortSignal) => Promise<void>) {
    participants.add(check);
    return () => { participants.delete(check); };
}
export async function reconcileRoutineParticipants(signal: AbortSignal) {
    for (const check of participants) {
        if (signal.aborted) throw new DOMException('Obsolete check', 'AbortError');
        await check(signal);
    }
}
