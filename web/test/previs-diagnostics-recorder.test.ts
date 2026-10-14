import { beforeEach, describe, expect, test } from "bun:test";

import { recordPrevisDiagnostic, resetPrevisDiagnosticDedupe } from "../src/lib/canvas/previs/previs-diagnostics-recorder";
import { getClientDiagnosticEvents } from "../src/services/diagnostics/client-diagnostics";

/** 只看预演台自己的稳定码事件，避免与其它测试写入的诊断流互相干扰。 */
function previsEvents() {
    return getClientDiagnosticEvents().filter((event) => (event.code || "").startsWith("PREVIS_"));
}

function newestPrevisEvent() {
    return previsEvents().at(-1);
}

const LEAK_PROBES = [
    "https://cdn.example.com/actor.glb?token=abc123#frag",
    "authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig",
    "cookie=session=deadbeef; Path=/",
    "api_key=sk-live-0000",
    "Error: boom\n    at PrevisModel (previs-viewport.tsx:571:29)",
    "演员 1 的镜头意图正文",
];

beforeEach(() => {
    resetPrevisDiagnosticDedupe();
});

describe("记录进统一诊断缓冲区", () => {
    test("写入的事件带稳定码、固定文案与 runtime 分类，且不落 stack", () => {
        const before = previsEvents().length;
        expect(recordPrevisDiagnostic("PREVIS_VIEWPORT_RENDER_FAILED")).toBe(true);

        expect(previsEvents().length).toBe(before + 1);
        const event = newestPrevisEvent();
        expect(event?.code).toBe("PREVIS_VIEWPORT_RENDER_FAILED");
        expect(event?.level).toBe("error");
        expect(event?.category).toBe("runtime");
        expect(event?.message).toBe("预演台 3D 视口渲染失败");
        expect(event?.stack || "").toBe("");
    });

    test("结构化字段进入 code 后缀，便于检索而不写自由文本", () => {
        const before = previsEvents().length;
        expect(recordPrevisDiagnostic("PREVIS_SAVE_FLUSH_FAILED", { revision: 3, sceneId: "scene-1" })).toBe(true);
        expect(previsEvents().length).toBe(before + 1);
        const event = newestPrevisEvent();
        expect(event?.code).toBe("PREVIS_SAVE_FLUSH_FAILED scene=scene-1 revision=3");
        expect(event?.level).toBe("error");
        expect(event?.message).toBe("预演台场景保存失败");
    });

    test("同一载荷在去重窗口内只记一次，避免刷新循环灌满缓冲区", () => {
        const before = previsEvents().length;
        expect(recordPrevisDiagnostic("PREVIS_SAVE_FLUSH_FAILED", { revision: 3 })).toBe(true);
        expect(previsEvents().length).toBe(before + 1);
        expect(recordPrevisDiagnostic("PREVIS_SAVE_FLUSH_FAILED", { revision: 3 })).toBe(false);
        expect(previsEvents().length).toBe(before + 1);
        // 载荷变化必须能再记一条，否则真实故障升级会被吞掉。
        expect(recordPrevisDiagnostic("PREVIS_SAVE_FLUSH_FAILED", { revision: 4 })).toBe(true);
        expect(previsEvents().length).toBe(before + 2);
    });

    test("未知 code 一律拒绝，不写进缓冲区", () => {
        const before = previsEvents().length;
        // @ts-expect-error 故意传入白名单外的代码，验证运行时拒绝。
        expect(recordPrevisDiagnostic("PREVIS_NOT_A_REAL_CODE")).toBe(false);
        expect(previsEvents().length).toBe(before);
    });

    test("泄露探针：URL、凭证、堆栈与业务正文都不进事件流", () => {
        for (const probe of LEAK_PROBES) {
            resetPrevisDiagnosticDedupe();
            recordPrevisDiagnostic("PREVIS_SAVE_FLUSH_FAILED", { sceneId: probe, objectId: probe });
            const serialized = JSON.stringify(previsEvents());
            expect(serialized).not.toContain(probe);
        }
    });
});
