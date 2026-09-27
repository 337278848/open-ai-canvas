import { expect, test } from "bun:test";
import { fileURLToPath } from "node:url";
import { runInNewContext } from "node:vm";

import { isIsolatedDirectorRepro } from "../src/lib/dev-repro";

// Execute the real entry point; replace only its font, network and UI side effects.
async function prepareEntry(dev, pathname) {
    const build = await Bun.build({
        entrypoints: [fileURLToPath(new URL("../src/main.tsx", import.meta.url))],
        target: "browser",
        format: "iife",
        define: { "import.meta.env.DEV": JSON.stringify(dev) },
        plugins: [
            {
                name: "entry-side-effects",
                setup(builder) {
                    builder.onResolve({ filter: /^(@fontsource-variable\/|@\/services\/appearance-bootstrap$|\.\/(welcome-)?application$)/ }, ({ path }) => ({ path, namespace: "entry-test" }));
                    builder.onLoad({ filter: /.*/, namespace: "entry-test" }, ({ path }) => {
                        if (path.startsWith("@fontsource-variable/")) return { contents: "", loader: "js" };
                        if (path === "@/services/appearance-bootstrap") {
                            return { contents: 'export function bootstrapAppearance() { events.push("appearance"); return appearanceReady; }', loader: "js" };
                        }
                        return { contents: `events.push(${JSON.stringify(path)}); entryLoaded();`, loader: "js" };
                    });
                },
            },
        ],
    });
    expect(build.success).toBe(true);

    const events = [];
    let resolveAppearance;
    let entryLoaded;
    const appearanceReady = new Promise((resolve) => (resolveAppearance = resolve));
    const loaded = new Promise((resolve) => (entryLoaded = resolve));
    const listeners = new Map();
    runInNewContext(await build.outputs[0].text(), { window: { location: { pathname }, addEventListener: (name, listener) => listeners.set(name, listener) }, events, appearanceReady, entryLoaded });
    expect(typeof listeners.get("vite:preloadError")).toBe("function");
    return { events, resolveAppearance, loaded };
}

async function prepareWelcomeEntry() {
    const build = await Bun.build({
        entrypoints: [fileURLToPath(new URL("../src/welcome-application.tsx", import.meta.url))],
        target: "browser",
        format: "iife",
        plugins: [
            {
                name: "welcome-entry-side-effects",
                setup(builder) {
                    builder.onResolve({ filter: /^(react|react\/jsx-runtime|react\/jsx-dev-runtime|react-dom\/client|@\/services\/api\/welcome|@\/services\/appearance-bootstrap|@\/stores\/use-appearance-store|@\/pages\/welcome)$/ }, ({ path }) => ({ path, namespace: "welcome-entry-test" }));
                    builder.onLoad({ filter: /.*/, namespace: "welcome-entry-test" }, ({ path }) => {
                        const sources = {
                            react: 'export const StrictMode = "StrictMode";',
                            "react/jsx-runtime": 'export const Fragment = "Fragment"; export function jsx(type, props) { return { type, props }; } export const jsxs = jsx;',
                            "react/jsx-dev-runtime": 'export const Fragment = "Fragment"; export function jsxDEV(type, props) { return { type, props }; }',
                            "react-dom/client": `
                                export function createRoot() {
                                    return {
                                        render() {
                                            state.rendered = true;
                                            state.renderedAfterAppearance = state.appearanceCommitted;
                                            state.events.push(state.appearanceCommitted ? "render:after-appearance" : "render:before-appearance");
                                        },
                                    };
                                }
                            `,
                            "@/services/api/welcome": 'export function getWelcomeAvailability() { state.events.push("availability:start"); return state.availabilityReady; }',
                            "@/services/appearance-bootstrap": `
                                export function bootstrapAppearance() {
                                    state.events.push("appearance:start");
                                    return state.appearanceReady.then(
                                        () => {
                                            state.events.push("appearance:committed");
                                            state.appearanceCommitted = true;
                                        },
                                        () => {
                                            state.events.push("appearance:failed");
                                            throw new Error("appearance failed");
                                        },
                                    );
                                }
                            `,
                            "@/stores/use-appearance-store": `
                                export const DEFAULT_PUBLIC_APPEARANCE = { brandName: "内置默认外观" };
                                export function commitPublicAppearance(value) {
                                    state.events.push("appearance:fallback-committed");
                                    state.appearanceCommitted = true;
                                    state.committedBrand = value.brandName;
                                    return value;
                                }
                            `,
                            "@/pages/welcome": 'export default function WelcomePage() { return "welcome"; }',
                        };
                        return { contents: sources[path], loader: "js" };
                    });
                },
            },
        ],
    });
    expect(build.success).toBe(true);

    const state = {
        events: [],
        appearanceCommitted: false,
        rendered: false,
        renderedAfterAppearance: false,
        committedBrand: null,
    };
    let resolveAvailability;
    let resolveAppearance;
    let rejectAppearance;
    state.availabilityReady = new Promise((resolve) => (resolveAvailability = resolve));
    state.appearanceReady = new Promise((resolve, reject) => {
        resolveAppearance = resolve;
        rejectAppearance = reject;
    });
    runInNewContext(await build.outputs[0].text(), {
        window: { location: { replace: () => state.events.push("redirect") } },
        document: { getElementById: () => ({}) },
        state,
        console: { error: () => undefined },
    });
    return { state, resolveAvailability, resolveAppearance, rejectAppearance };
}

async function flushWelcomeEntry() {
    for (let index = 0; index < 4; index += 1) await new Promise((resolve) => setTimeout(resolve, 0));
}

test("DEV director lab loads without calling the appearance backend", async () => {
    const entry = await prepareEntry(true, "/dev/director-repro");
    await entry.loaded;
    expect(entry.events).toEqual(["./application"]);
});

for (const [dev, pathname] of [
    [false, "/dev/director-repro"],
    [true, "/login"],
    [false, "/login"],
    [true, "/dev/director-repro/"],
    [true, "/dev/director-repro-other"],
]) {
    test(`appearance loads in parallel with normal startup: dev=${dev} path=${pathname}`, async () => {
        const entry = await prepareEntry(dev, pathname);
        await entry.loaded;
        expect(entry.events).toEqual(["appearance", "./application"]);
        entry.resolveAppearance();
    });
}

for (const pathname of ["/welcome", "/welcome/"]) {
    test(`public film entry remains independent: ${pathname}`, async () => {
        const entry = await prepareEntry(false, pathname);
        await entry.loaded;
        expect(entry.events).toEqual(["./welcome-application"]);
    });
}

test("direct welcome entry starts availability and appearance together, then renders after appearance commits", async () => {
    const entry = await prepareWelcomeEntry();
    expect(entry.state.events).toEqual(["appearance:start", "availability:start"]);

    entry.resolveAvailability({ welcomeEnabled: true });
    await flushWelcomeEntry();
    expect(entry.state.rendered).toBe(false);

    entry.resolveAppearance();
    await flushWelcomeEntry();
    expect(entry.state.events).toEqual(["appearance:start", "availability:start", "appearance:committed", "render:after-appearance"]);
    expect(entry.state.renderedAfterAppearance).toBe(true);
});

test("direct welcome entry commits the built-in appearance fallback before rendering when appearance bootstrap fails", async () => {
    const entry = await prepareWelcomeEntry();
    entry.resolveAvailability({ welcomeEnabled: true });
    entry.rejectAppearance(new Error("network unavailable"));
    await flushWelcomeEntry();

    expect(entry.state.events).toEqual(["appearance:start", "availability:start", "appearance:failed", "appearance:fallback-committed", "render:after-appearance"]);
    expect(entry.state.committedBrand).toBe("内置默认外观");
});

test("provider isolation shares the exact DEV-only route boundary", () => {
    expect(isIsolatedDirectorRepro(true, "/dev/director-repro")).toBe(true);
    expect(isIsolatedDirectorRepro(false, "/dev/director-repro")).toBe(false);
    for (const path of ["/", "/login", "/dev/director-repro/", "/dev/director-repro-other"]) {
        expect(isIsolatedDirectorRepro(true, path)).toBe(false);
    }
});
