import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const changelog = readFileSync(new URL("../../CHANGELOG.md", import.meta.url), "utf8");

describe("public changelog", () => {
    test("uses product release language instead of source-control provenance", () => {
        for (const forbidden of [
            /PR\s*#/i,
            /原作者/,
            /GitHub Release/i,
            /github\.com\//i,
            /ghcr\.io\//i,
            /官方 main/,
            /本地定制/,
            /项目贡献者列表/,
        ]) {
            expect(changelog).not.toMatch(forbidden);
        }
    });

    test("keeps the current release notes available to the in-app dialog", () => {
        expect(changelog).toContain("## v1.5.7");
        expect(changelog).toContain("动态多维表格");
        expect(changelog).toContain("Wan3");
    });
});
