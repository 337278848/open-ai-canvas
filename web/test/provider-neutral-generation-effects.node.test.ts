import assert from "node:assert/strict";
import { test } from "node:test";

import { createProviderNeutralGenerationTaskEffectStore } from "../src/services/provider-neutral-generation-effects";

test("non-secure origins explain that HTTPS is required for generation result locking", async () => {
    const originalWindow = (globalThis as { window?: unknown }).window;
    const originalNavigator = (globalThis as { navigator?: unknown }).navigator;
    Object.defineProperty(globalThis, "window", {
        configurable: true,
        value: {
            isSecureContext: false,
            localStorage: {
                getItem: () => null,
            },
        },
    });
    Object.defineProperty(globalThis, "navigator", { configurable: true, value: {} });
    try {
        const store = createProviderNeutralGenerationTaskEffectStore();
        await assert.rejects(store.claim("effect-1", "task-1"), /未启用 HTTPS/);
    } finally {
        if (originalWindow === undefined) delete (globalThis as { window?: unknown }).window;
        else Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow });
        if (originalNavigator === undefined) delete (globalThis as { navigator?: unknown }).navigator;
        else Object.defineProperty(globalThis, "navigator", { configurable: true, value: originalNavigator });
    }
});
