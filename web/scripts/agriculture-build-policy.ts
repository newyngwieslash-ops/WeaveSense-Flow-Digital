import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, relative, resolve } from "node:path";
import type { Plugin } from "vite";

export function assertAgricultureModule(id: string) {
    const path = id.replaceAll("\\", "/");
    if (/(?:^|\/)(?:node_modules\/)?(?:@tldraw\/|tldraw(?:\/|$))/.test(path) || /\/src\/(?:pages\/canvas|components\/canvas\/drawing)/.test(path)) {
        throw new Error(`Agriculture production excludes drawing implementation: ${id}`);
    }
}

export function agricultureBuildPolicy(webRoot: string): Plugin {
    return {
        name: "agriculture-production-boundary",
        apply: "build",
        moduleParsed(module) { assertAgricultureModule(module.id); },
        generateBundle(_, bundle) {
            const modules = new Set<string>();
            for (const output of Object.values(bundle)) {
                if (output.type !== "chunk") continue;
                for (const id of Object.keys(output.modules)) {
                    assertAgricultureModule(id);
                    modules.add(id);
                }
            }
            const packages = new Map<string, { name: string; version: string; license: string }>();
            const notices = new Map<string, string>();
            for (const id of modules) {
                const normalized = id.replaceAll("\\", "/");
                if (!normalized.includes("/node_modules/")) continue;
                let directory = dirname(id.split("?")[0]);
                while (directory.includes("node_modules")) {
                    try {
                        const pkg = JSON.parse(readFileSync(resolve(directory, "package.json"), "utf8"));
                        if (pkg.name && pkg.version) {
                            const key = `${pkg.name}@${pkg.version}`;
                            packages.set(key, { name: pkg.name, version: pkg.version, license: typeof pkg.license === "string" ? pkg.license : "REVIEW_REQUIRED" });
                            const files = readdirSync(directory).filter((name) => /^(?:license|licence|notice|ofl)(?:\.|$)/i.test(name));
                            if (files.length) notices.set(key, files.map((name) => `${name}\n${readFileSync(resolve(directory, name), "utf8")}`).join("\n\n"));
                            else if (key === "is-mobile@5.0.0") {
                                const readme = readFileSync(resolve(directory, "README.md"), "utf8");
                                const license = readme.slice(readme.indexOf("## License"));
                                if (!license.includes("Permission is hereby granted")) throw new Error("is-mobile license text changed");
                                notices.set(key, license);
                            } else if (key === "@ant-design/icons-svg@4.5.0") {
                                notices.set(key, readFileSync(resolve(webRoot, "../docs/agriculture/profile/licenses/ant-design-icons-svg-4.5.0.LICENSE"), "utf8"));
                            }
                            break;
                        }
                    } catch { /* Bundle helper modules may not own a package manifest. */ }
                    const parent = dirname(directory);
                    if (parent === directory) break;
                    directory = parent;
                }
            }
            let sourceCommit = process.env.CANVAS_BUILD_COMMIT || "unknown";
            let workingTreeModified = true;
            try {
                sourceCommit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: webRoot, encoding: "utf8" }).trim();
                workingTreeModified = Boolean(execFileSync("git", ["status", "--porcelain", "--", "."], { cwd: webRoot, encoding: "utf8" }).trim());
            } catch { /* Source archives must explicitly supply build provenance. */ }
            const graph = {
                schema_version: "1.0.0", product_profile: "agriculture", data_class: "build_evidence",
                source_commit: sourceCommit, working_tree_modified: workingTreeModified,
                lock_sha256: createHash("sha256").update(readFileSync(resolve(webRoot, "bun.lock"))).digest("hex"),
                modules: [...modules].map((id) => relative(webRoot, id).replaceAll("\\", "/")).sort(),
                packages: [...packages.values()].sort((a, b) => a.name.localeCompare(b.name)),
            };
            for (const key of packages.keys()) {
                if (!notices.has(key)) throw new Error(`Missing production license text: ${key}`);
            }
            this.emitFile({ type: "asset", fileName: "agriculture-build-graph.json", source: JSON.stringify(graph, null, 2) + "\n" });
            this.emitFile({ type: "asset", fileName: "THIRD_PARTY_NOTICES.txt", source: [...notices].sort(([a], [b]) => a.localeCompare(b)).map(([key, text]) => `===== ${key} =====\n${text}`).join("\n\n") });
        },
    };
}
