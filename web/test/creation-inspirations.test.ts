import { describe, expect, test } from "bun:test";
import { existsSync } from "node:fs";
import { resolve } from "node:path";
import { creationFeaturedWorks } from "../src/pages/create/creation-inspirations";

describe("curated creation inspirations", () => {
    test("all templates have unique titles, usable prompts and local cover assets", () => {
        expect(creationFeaturedWorks.length).toBe(22);
        expect(new Set(creationFeaturedWorks.map((item) => item.title)).size).toBe(22);
        for (const item of creationFeaturedWorks) {
            expect(["image", "video", "text"]).toContain(item.mode);
            expect(item.prompt.length).toBeGreaterThan(35);
            expect(existsSync(resolve(import.meta.dir, "../public", item.image.slice(1)))).toBe(true);
        }
    });
    test("adapted prompts stay distinguishable from original platform prompts", () => {
        expect(creationFeaturedWorks.filter((item) => item.adapted).length).toBe(8);
    });
});
