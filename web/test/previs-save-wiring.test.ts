import { describe, expect, it } from "bun:test";

import type { PrevisSaveSnapshot } from "../src/lib/canvas/previs/previs-save";
import {
    describePrevisSaveStatus,
    idlePrevisSaveProgress,
    previsSaveProgress,
    resolvePrevisCloseOutcome,
    shouldBlockPrevisUnload,
} from "../src/lib/canvas/previs/previs-save-wiring";

function snapshot(revision: number, confirmedRevision: number, status: PrevisSaveSnapshot["status"]): PrevisSaveSnapshot {
    return { revision, confirmedRevision, status };
}

describe("previs 保存接线（无头快照投影）", () => {
    it("空闲快照就是已保存状态", () => {
        expect(idlePrevisSaveProgress).toEqual({ revision: 0, confirmedRevision: 0, status: "saved" });
        expect(shouldBlockPrevisUnload(idlePrevisSaveProgress)).toBe(false);
    });

    it("快照投影只保留 revision/confirmedRevision/status 三项", () => {
        const projected = previsSaveProgress(snapshot(3, 1, "saving"));
        expect(projected).toEqual({ revision: 3, confirmedRevision: 1, status: "saving" });
        expect(Object.keys(projected).sort()).toEqual(["confirmedRevision", "revision", "status"]);
    });

    it("状态指示灯覆盖 idle / pending / danger 与忙碌态", () => {
        expect(describePrevisSaveStatus(idlePrevisSaveProgress)).toEqual({ label: "已保存", tone: "idle", busy: false });
        expect(describePrevisSaveStatus(snapshot(1, 0, "saving"))).toEqual({ label: "正在保存…", tone: "pending", busy: true });
        expect(describePrevisSaveStatus(snapshot(1, 0, "dirty"))).toEqual({ label: "有未确认操作，后台处理中", tone: "pending", busy: false });
        expect(describePrevisSaveStatus(snapshot(1, 0, "error"))).toEqual({
            label: "保存暂时未完成 · 未确认操作将在后台继续处理",
            tone: "danger",
            busy: false,
        });
    });

    it("全部确认且状态回到 saved 才显示成功", () => {
        expect(describePrevisSaveStatus(snapshot(2, 2, "saved"))).toEqual({ label: "已保存", tone: "idle", busy: false });
        // dirty 即使 revision 已追平也要显示待处理，直到状态机自己回到 saved。
        expect(describePrevisSaveStatus(snapshot(2, 2, "dirty")).tone).toBe("pending");
    });

    it("未确认的 revision 一定显示待处理，即使状态早已回到 saved", () => {
        const stale = snapshot(2, 1, "saved");
        expect(describePrevisSaveStatus(stale).tone).toBe("pending");
        expect(shouldBlockPrevisUnload(stale)).toBe(true);
    });

    it("关闭决策：已确认才放行，否则给出保留草稿的说明", () => {
        expect(resolvePrevisCloseOutcome("close")).toEqual({ kind: "close" });
        const blocked = resolvePrevisCloseOutcome("stay");
        expect(blocked.kind).toBe("blocked");
        if (blocked.kind === "blocked") {
            expect(blocked.message).toContain("保存暂时未完成");
            expect(blocked.message).toContain("后台");
        }
    });

    it("错误状态即使 revision 已确认也要阻止卸载，避免丢结果", () => {
        expect(shouldBlockPrevisUnload(snapshot(1, 1, "error"))).toBe(true);
        expect(shouldBlockPrevisUnload(snapshot(1, 1, "saving"))).toBe(false);
    });
});
