import { describe, expect, it } from "bun:test";

import { agentTurnParentId, isAgentRunTerminalForUser, isRunScopedError, recoverableAgentPrompt, withoutFailedTurnErrors } from "@/lib/canvas/agent-turn-recovery";
import type { AgentRun } from "@/services/api/agent";

function agentRun(status: AgentRun["status"], extra: Partial<AgentRun> = {}): AgentRun {
    return { id: "run-current", canvasId: "canvas-1", permissionMode: "request_approval", status, createdAt: "2026-09-15T00:00:00Z", updatedAt: "2026-09-15T00:00:00Z", ...extra };
}

describe("failed Agent turn recovery", () => {
    it("treats only failed and cancelled runs as recoverable terminals", () => {
        expect(isAgentRunTerminalForUser(agentRun("failed"))).toBe(true);
        expect(isAgentRunTerminalForUser(agentRun("cancelled"))).toBe(true);
        for (const status of ["queued", "running", "waiting_approval", "completed"] as const) {
            expect(isAgentRunTerminalForUser(agentRun(status))).toBe(false);
        }
        expect(isAgentRunTerminalForUser(null)).toBe(false);
        expect(isAgentRunTerminalForUser(undefined)).toBe(false);
    });

    it("continues from the previous clean turn instead of the failed one", () => {
        // 失败轮会把自身的提示词和失败摘要拼进下一轮历史，必须挂回它父轮。
        expect(agentTurnParentId(agentRun("failed", { id: "run-failed", parentId: "run-clean" }))).toBe("run-clean");
        expect(agentTurnParentId(agentRun("cancelled", { id: "run-cancelled", parentId: "run-clean" }))).toBe("run-clean");
        // 第一轮就失败时没有父轮，应作为全新根轮次重新开始。
        expect(agentTurnParentId(agentRun("failed", { id: "run-failed" }))).toBeUndefined();
        // 正常轮次仍然作为父轮，继续对话链。
        expect(agentTurnParentId(agentRun("completed", { id: "run-done" }))).toBe("run-done");
        expect(agentTurnParentId(null)).toBeUndefined();
    });

    it("walks back past a chain of consecutive failures", () => {
        // 复刻线上时间线：成功轮 -> 失败 -> 失败。连续失败时，失败轮的
        // parentId 本身就是另一个失败轮，只回溯一级仍会继承失败事实，
        // 所以必须用最后一个成功轮次作为父轮。
        const secondFailure = agentRun("failed", { id: "run-failed-2", parentId: "run-failed-1" });
        expect(agentTurnParentId(secondFailure)).toBe("run-failed-1");
        expect(agentTurnParentId(secondFailure, "run-clean")).toBe("run-clean");
        // 干净轮次就是自己时不能自指，否则会退化成"没有父轮"。
        expect(agentTurnParentId(agentRun("failed", { id: "run-clean", parentId: "run-x" }), "run-clean")).toBe("run-x");
        // 干净轮次未知时退回一级回溯。
        expect(agentTurnParentId(secondFailure, undefined)).toBe("run-failed-1");
    });

    it("recovers the last user prompt, falling back to the current draft", () => {
        const messages = [
            { role: "user" as const, text: "第一轮" },
            { role: "assistant" as const, text: "回答" },
            { role: "user" as const, text: "  适合视频模型的完整生成提示词  " },
            { role: "error" as const, text: "Agent 执行失败" },
        ];
        expect(recoverableAgentPrompt(messages, "草稿")).toBe("适合视频模型的完整生成提示词");
        expect(recoverableAgentPrompt([], "  继续  ")).toBe("继续");
        expect(recoverableAgentPrompt([{ role: "assistant", text: "只有回答" }], "")).toBe("");
    });

    it("clears only this run's error bubbles, keeping unrelated failures", () => {
        const messages = [
            { id: "user-1", role: "user" as const, text: "写一段提示词" },
            { id: "run-failed:3", role: "error" as const, text: "模型任务未成功" },
            { id: "terminal-run-failed", role: "error" as const, text: "上游模型服务暂时过载" },
            // 与本次运行无关的功能性报错不能被重发顺手抹掉。
            { id: "skills-load-error", role: "error" as const, text: "技能库读取失败" },
            { id: "canvas-sync-canvas-1", role: "error" as const, text: "画布同步冲突" },
        ];
        expect(isRunScopedError({ id: "run-failed:3", role: "error", text: "" }, "run-failed")).toBe(true);
        expect(isRunScopedError({ id: "run-other:3", role: "error", text: "" }, "run-failed")).toBe(false);
        expect(isRunScopedError({ id: "skills-load-error", role: "error", text: "" }, "run-failed")).toBe(false);
        expect(isRunScopedError({ id: "terminal-x", role: "assistant", text: "" }, "run-failed")).toBe(false);

        const kept = withoutFailedTurnErrors(messages, "run-failed");
        expect(kept.map((item) => item.id)).toEqual(["user-1", "skills-load-error", "canvas-sync-canvas-1"]);
    });
});
