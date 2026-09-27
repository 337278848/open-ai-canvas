import { describe, expect, test } from "bun:test";

import { createClientId, createSecureId } from "../src/lib/client-id";

describe("createClientId", () => {
    test("生成不依赖 randomUUID 的唯一客户端标识", () => {
        const ids = Array.from({ length: 100 }, () => createClientId());

        expect(new Set(ids).size).toBe(ids.length);
        ids.forEach((id) => expect(id).toMatch(/^[A-Za-z0-9_-]{21}$/));
    });
});

describe("createSecureId", () => {
    test("randomUUID 缺失时仍生成唯一安全 ID", () => {
        const originalCrypto = globalThis.crypto;
        const originalRandomUUID = originalCrypto?.randomUUID;
        if (originalCrypto) {
            Object.defineProperty(originalCrypto, "randomUUID", {
                configurable: true,
                value: undefined,
            });
        }
        try {
            const ids = Array.from({ length: 100 }, () => createSecureId());

            expect(new Set(ids).size).toBe(ids.length);
            ids.forEach((id) => expect(id).toMatch(/^[A-Za-z0-9_-]{32}$/));
        } finally {
            if (originalCrypto) {
                Object.defineProperty(originalCrypto, "randomUUID", {
                    configurable: true,
                    value: originalRandomUUID,
                });
            }
        }
    });
});
