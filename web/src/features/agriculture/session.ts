import type { AuthSessionPayload } from "@/services/api/auth";
import { appQueryClient } from "@/lib/query-client";
import { setActiveUserScope } from "@/lib/user-scope";
import { useUserStore } from "@/stores/use-user-store";

let sessionGeneration = 0;

// The agriculture product shares the platform cookie/API and user identity. Film
// canvas persistence is not initialized in this product's dependency graph.
export async function applyUserSession(payload: AuthSessionPayload) {
    const generation = ++sessionGeneration;
    const store = useUserStore.getState();
    store.setHydrated(false);
    await appQueryClient.cancelQueries({ queryKey: ["agriculture"] });
    if (generation !== sessionGeneration) return;
    // Membership can change without changing the user ID. Never rehydrate the
    // agricultural shell with a previously fresh workspace/resource projection.
    appQueryClient.removeQueries({ queryKey: ["agriculture"] });
    if (useUserStore.getState().user?.id !== payload.user?.id) {
        appQueryClient.clear();
    }
    setActiveUserScope(payload.user?.id);
    store.setUser(payload.user);
    store.setRuntimeLimits(payload.runtimeLimits);
    store.setFeatures(payload.features);
    store.setHydrated(true);
}
