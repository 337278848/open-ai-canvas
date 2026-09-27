import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

function readProjectFile(relativePath: string) {
    return readFileSync(resolve(import.meta.dir, "../../", relativePath), "utf8");
}

function serviceBlock(compose: string, service: string) {
    const match = compose.match(new RegExp(`^  ${service}:\\r?\\n([\\s\\S]*?)(?=^  \\S.*$)`, "m"));
    expect(match).not.toBeNull();
    return match?.[1] ?? "";
}

describe("后台系统更新路由契约", () => {
    test("保留系统更新菜单对应的页面路由", () => {
        const router = readProjectFile("web/src/router.tsx");
        const adminShell = readProjectFile("web/src/pages/admin/components/admin-shell.tsx");

        expect(adminShell).toContain('path: "/admin/settings/system-update"');
        expect(router).toContain('const SystemUpdatePage = lazy(() => import("@/pages/admin/settings/system-update-page"))');
        expect(router).toContain('{ path: "settings/system-update", element: deferred(<SystemUpdatePage />) }');
    });
});

describe("Compose 上游素材中转配置", () => {
    for (const composeFile of ["docker-compose.deploy.yml", "docker-compose.server.yml"]) {
        test(`${composeFile} 将 CANVAS_UPSTREAM_MEDIA_RELAY 传入 backend`, () => {
            const compose = readProjectFile(composeFile);
            const backend = serviceBlock(compose, "backend");

            expect(backend).toContain("environment:");
            expect(backend).toContain("CANVAS_UPSTREAM_MEDIA_RELAY: ${CANVAS_UPSTREAM_MEDIA_RELAY:-}");
        });
    }
});
