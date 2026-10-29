// Test-harness document barrier, not application authentication or routing.
export function navigationCommitted(frameTree, navigation) {
    const frame = frameTree?.frameTree?.frame;
    return typeof navigation?.loaderId === "string" && navigation.loaderId.length > 0
        && typeof navigation.frameId === "string" && navigation.frameId.length > 0
        && frame?.id === navigation.frameId && frame?.loaderId === navigation.loaderId;
}

export async function navigateDocument({ send, evaluate, destination, onCommit = () => {} }) {
    const deadline = Date.now() + 20000;
    onCommit(false);
    const navigation = await send("Page.navigate", { url: destination });
    if (!navigation?.loaderId || !navigation.frameId || navigation.errorText) {
        throw new Error("browser document navigation did not return an expected loader");
    }
    // A repeated same-URL navigation has an old, already-complete document with
    // exactly the requested performance name. Only the returned loader identifies
    // the new document; do not inspect DOM readiness before its actual commit.
    while (Date.now() < deadline) {
        try {
            const committed = navigationCommitted(await send("Page.getFrameTree"), navigation);
            onCommit(committed);
            if (committed && await evaluate(`document.readyState === 'complete' && performance.getEntriesByType('navigation')[0]?.name === ${JSON.stringify(destination)}`)) return;
        } catch { /* execution context can change during the new document commit */ }
        await new Promise(resolve => setTimeout(resolve, 100));
    }
    throw new Error("browser assertion timeout: expected document loader committed and complete");
}
