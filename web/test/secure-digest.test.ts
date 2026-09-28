import { describe, expect, test } from "bun:test";

import { sha256Hex } from "../src/lib/secure-digest";

describe("sha256Hex", () => {
    test("Web Crypto 不可用时仍生成稳定输入标识", async () => {
        const first = await sha256Hex("generation-retry\0group\0task", null);
        const second = await sha256Hex("generation-retry\0group\0task", null);
        const other = await sha256Hex("generation-retry\0group\0other", null);

        expect(first).toBe(second);
        expect(first).not.toBe(other);
        expect(first).toMatch(/^fnv1a:/);
    });
});
