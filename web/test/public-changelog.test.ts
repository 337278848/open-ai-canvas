import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { publicReleaseNotes } from "../src/lib/public-release-notes";

const changelog = readFileSync(new URL("../../CHANGELOG.md", import.meta.url), "utf8");

describe("public changelog", () => {
    test("uses product release language instead of source-control provenance", () => {
        for (const forbidden of [/PR\s*#/i, /原作者/, /主分支/, /上游/, /fork/i, /开源/, /GitHub Release/i, /GitHub 预发布/i, /Host Updater/i, /github\.com\//i, /ghcr\.io\//i, /官方 main/, /本地定制/, /项目贡献者列表/]) {
            expect(changelog).not.toMatch(forbidden);
        }
    });

    test("keeps the current release notes available to the in-app dialog", () => {
        expect(changelog).toContain("## Unreleased");
        expect(changelog).toContain("模型服务连接失败被误报为图片生成等待超时");
        expect(changelog).toContain("## v1.5.9");
        expect(changelog).toContain("拼好布");
        expect(changelog).not.toContain("## v1.5.8");
        expect(changelog).not.toContain("## v1.5.7");
    });

    test("filters provenance from remote or stale release notes at render time", () => {
        const notes = publicReleaseNotes(`# CHANGELOG

## v1.5.8

- 合并 PR #600（原作者 @someone）：内部实现。
- 新增批量镜头生成。
- 镜像地址 https://ghcr.io/example/project。
`);
        expect(notes).toContain("新增批量镜头生成");
        expect(notes).not.toMatch(/PR\s*#|原作者|ghcr\.io/i);
    });

    test("the public welcome page contains no repository or open-source origin link", () => {
        const welcome = readFileSync(new URL("../src/pages/welcome/index.tsx", import.meta.url), "utf8");
        expect(welcome).not.toMatch(/github\.com|Open Source|MIT License|开源 AI|贡献者/);
        expect(welcome).toContain("AI 影视创作工作台");
    });

    test("customer-facing discovery pages do not expose platform repository provenance", () => {
        const creation = readFileSync(new URL("../src/pages/create/creation-workspace.tsx", import.meta.url), "utf8");
        const plugins = readFileSync(new URL("../src/pages/plugins/index.tsx", import.meta.url), "utf8");
        const pluginDocs = readFileSync(new URL("../src/pages/plugins/plugin-documentation.ts", import.meta.url), "utf8");
        const skills = readFileSync(new URL("../src/pages/skills/index.tsx", import.meta.url), "utf8");
        expect(creation).not.toMatch(/github\.com|awesome-chatgpt-prompts|开源改编/);
        expect(plugins).not.toMatch(/官方插件|第三方插件|全部来源/);
        expect(pluginDocs).not.toMatch(/作者：|联系插件作者/);
        expect(skills).not.toContain("GitHub 技能同步失败");
    });
});
