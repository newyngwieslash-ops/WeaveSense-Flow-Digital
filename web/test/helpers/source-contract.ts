// Formatting-independent source contracts.
//
// Some tests pin wiring that is hard to observe without a full browser (which
// handler a toolbar button gets, which route renders which page). They used to
// compare raw source text, so running Prettier on the file broke them. These
// helpers compare code after removing the formatting Prettier is allowed to
// change: whitespace/line breaks, trailing commas, optional parentheses around
// a single arrow parameter and semicolons before a closing brace and JSX wrapping parentheses. Identifiers, strings,
// operators and structure must still match exactly.

import { expect } from "bun:test";

/** Canonical form of a code fragment, independent of Prettier formatting. */
export function code(source: string): string {
    return (
        source
            .replace(/\s+/g, "")
            .replace(/,(?=[)\]}>])/g, "")
            .replace(/\(([A-Za-z_$][\w$]*)\)=>/g, "$1=>")
            .replace(/;(?=[})])/g, "")
            // JSX Prettier wraps in parentheses: `? (<A/>) : null`, `element: (<A/>)`, `=> (<A/>)`.
            .replace(/(\?|:|=>|&&|\|\||return)\(</g, "$1<")
            .replace(/>\)(?=[:},);\]]|$)/g, ">")
    );
}

/** Slice of `source` between two markers, compared in canonical form. */
export function codeSlice(source: string, from: string, to: string): string {
    const s = code(source);
    const start = s.indexOf(code(from).replace(/[;,]$/, ""));
    expect(start).toBeGreaterThanOrEqual(0);
    const end = s.indexOf(code(to), start + code(from).length);
    expect(end).toBeGreaterThan(start);
    return s.slice(start, end);
}

expect.extend({
    toContainCode(received: unknown, expected: string) {
        if (typeof received !== "string") {
            const pass = Array.isArray(received) && received.includes(expected);
            return { pass, message: () => `expected ${JSON.stringify(received)} ${pass ? "not " : ""}to contain ${expected}` };
        }
        // A fragment may end where the source has "…;}" (semicolon dropped before a brace).
        const pass = code(received).includes(code(expected).replace(/[;,]$/, ""));
        return { pass, message: () => `expected source ${pass ? "not " : ""}to contain code:\n  ${expected}` };
    },
});

declare module "bun:test" {
    interface Matchers<T> {
        toContainCode(expected: string): void;
    }
}
