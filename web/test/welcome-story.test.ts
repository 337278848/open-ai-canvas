import { describe, expect, test } from "bun:test";
import { existsSync } from "node:fs";
import { resolve } from "node:path";
import { chapters, getWelcomeLook, showcases, welcomeLooks } from "../src/pages/welcome/story";

const publicFile = (url: string) => resolve(import.meta.dir, "../public", url.replace(/^\//, ""));

describe("welcome story", () => {
    test("uses a neutral, media-free default experience", () => {
        expect(chapters[0].title).toBe("创作空间");
        expect(welcomeLooks.map((look) => look.id)).toEqual(["default"]);
        expect(getWelcomeLook("").id).toBe("default");
        expect(getWelcomeLook("?look=unknown").id).toBe("default");
        expect(welcomeLooks[0].frames).toEqual([]);
        expect(welcomeLooks[0].screenplay).toEqual([]);
        expect(welcomeLooks[0].video).toBeUndefined();
        expect(welcomeLooks[0].credit).toBeUndefined();
    });

    test("does not reference bundled demo media", () => {
        for (const showcase of showcases) expect(showcase.image).toBe("");
        expect(existsSync(publicFile("/logo.svg"))).toBe(false);
        expect(existsSync(publicFile("/welcome"))).toBe(false);
    });
});
