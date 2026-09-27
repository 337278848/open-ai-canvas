import { describe, expect, test } from "bun:test";

import { sha256Hex } from "../src/lib/secure-digest";

describe("sha256Hex", () => {
    test("Web Crypto 不可用时仍生成稳定输入标识", async () => {
        const originalSubtle = globalThis.crypto.subtle;
        Object.defineProperty(globalThis.crypto, "subtle", {
            configurable: true,
            value: undefined,
        });
        try {
            const first = await sha256Hex("generation-retry\0group\0task");
            const second = await sha256Hex("generation-retry\0group\0task");
            const other = await sha256Hex("generation-retry\0group\0other");

            expect(first).toBe(second);
            expect(first).not.toBe(other);
            expect(first).toMatch(/^fnv1a:/);
        } finally {
            Object.defineProperty(globalThis.crypto, "subtle", {
                configurable: true,
                value: originalSubtle,
            });
        }
    });
});
