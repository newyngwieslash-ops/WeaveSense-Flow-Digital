import { readFileSync, readdirSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { assertAgricultureModule } from "./agriculture-build-policy";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../dist-agriculture");
const graph = JSON.parse(readFileSync(resolve(root, "agriculture-build-graph.json"), "utf8"));
if (graph.product_profile !== "agriculture" || graph.modules.length === 0 || graph.packages.length === 0) throw new Error("Missing agriculture production dependency evidence");
for (const id of graph.modules) assertAgricultureModule(id);
for (const pkg of graph.packages) assertAgricultureModule(pkg.name);
for (const file of readdirSync(resolve(root, "assets"))) {
    if (!/\.(js|css)$/.test(file)) continue;
    const contents = readFileSync(resolve(root, "assets", file), "utf8");
    if (/tldraw\.com|tldraw\.css|TldrawEditor|TL_DRAW_VERSION|@tldraw\//.test(contents)) throw new Error(`Excluded drawing runtime found in ${file}`);
}
console.log(JSON.stringify({ profile: "agriculture", modules: graph.modules.length, packages: graph.packages.length, excluded_drawing_runtime: "absent", source_commit: graph.source_commit }));
