import { describe, expect, test } from "bun:test";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { relative, resolve } from "node:path";
import { publicReleaseNotes } from "../src/lib/public-release-notes";

const changelog = readFileSync(new URL("../../CHANGELOG.md", import.meta.url), "utf8");
const version = readFileSync(new URL("../../VERSION", import.meta.url), "utf8").trim();

describe("public changelog", () => {
    test("uses product release language instead of source-control provenance", () => {
        for (const forbidden of [/PR\s*#/i, /原作者/, /主分支/, /上游/, /fork/i, /开源/, /GitHub Release/i, /GitHub 预发布/i, /Host Updater/i, /github\.com\//i, /ghcr\.io\//i, /官方 main/, /本地定制/, /项目贡献者列表/]) {
            expect(changelog).not.toMatch(forbidden);
        }
    });

    test("keeps the current release notes available to the in-app dialog", () => {
        expect(changelog).toContain(`## ${version}`);
        for (const heading of changelog.match(/^## v\S+.*$/gm) ?? []) {
            expect(heading).toMatch(/^## v\S+ - \d{4}-\d{2}-\d{2}$/);
        }
        expect(changelog).toContain("## v1.6.0");
        expect(changelog).toContain("管理后台模型保存失败时");
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

    test("the previs workbench retains the product brand after synchronization", () => {
        const previs = readFileSync(new URL("../src/components/canvas/previs/canvas-previs-workbench.tsx", import.meta.url), "utf8");
        expect(previs).toContain('className="previs-desk-logo">拼好布</span>');
        expect(previs).not.toContain("影策");
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

    test("no shipped plugin artifact or source manifest leaks the legacy brand", () => {
        // 客户在插件中心能看到插件清单与文档，交付包里也带着这些文件；
        // 任何一处残留旧品牌都会直接暴露给最终用户。
        const pluginPackages = resolve(import.meta.dir, "../../plugin-packages");
        const offenders: string[] = [];
        const inspect = (file: string) => {
            const source = readFileSync(file, "utf8");
            if (source.includes("影策")) offenders.push(relative(pluginPackages, file).replaceAll("\\\\", "/"));
        };
        for (const entry of readdirSync(pluginPackages, { withFileTypes: true })) {
            if (entry.isDirectory()) {
                for (const name of ["manifest.json", "README.md"]) {
                    const candidate = resolve(pluginPackages, entry.name, name);
                    if (existsSync(candidate)) inspect(candidate);
                }
                const docs = resolve(pluginPackages, entry.name, "docs", "interface.md");
                if (existsSync(docs)) inspect(docs);
                continue;
            }
            if (!entry.name.endsWith(".yingce-plugin")) continue;
            // 分发包是 ZIP；只扫描未压缩的文本条目，命中即视为泄漏。
            const archive = readFileSync(resolve(pluginPackages, entry.name));
            const marker = Buffer.from("manifest.json", "utf8");
            if (archive.includes(Buffer.from("影策", "utf8")) && archive.includes(marker)) {
                offenders.push(entry.name);
            }
        }
        expect(offenders).toEqual([]);
    });
});
