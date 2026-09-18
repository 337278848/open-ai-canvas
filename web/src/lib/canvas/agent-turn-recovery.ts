import type { AgentRun } from "@/services/api/agent";

export type RecoverableAgentMessage = {
    id?: string;
    role: "user" | "assistant" | "system" | "tool" | "error";
    text: string;
};

/**
 * 运行级错误气泡 id 前缀。它们都描述"这一轮的运行状态"，重发成功后
 * 继续留在时间线里会让人以为新的一轮也失败了。
 * 技能库、画布同步、导出等功能性报错刻意不在此列，必须留给用户。
 */
const RUN_SCOPED_ERROR_PREFIXES = ["terminal-", "stream-error-", "submit-error-", "cancel-", "approval-error-"];

export function isRunScopedError(message: RecoverableAgentMessage, runId?: string) {
    if (message.role !== "error") return false;
    const id = message.id || "";
    if (RUN_SCOPED_ERROR_PREFIXES.some((prefix) => id.startsWith(prefix))) return true;
    // run_failed 事件直接用 `${runId}:${seq}` 当消息 id。
    return Boolean(runId) && id.startsWith(`${runId}:`);
}

/** 重发前清掉失败轮残留的运行级报错；非运行级报错原样保留。 */
export function withoutFailedTurnErrors<T extends RecoverableAgentMessage>(messages: T[], runId?: string): T[] {
    return messages.filter((message) => !isRunScopedError(message, runId));
}

export function isAgentRunTerminalForUser(run: AgentRun | null | undefined) {
    return run?.status === "failed" || run?.status === "cancelled";
}

/**
 * 失败轮次不是干净的上下文父节点：服务端会把失败轮的提示词和失败摘要
 * 一起拼进下一轮历史，模型就会把"已经试过并失败"当成事实。
 * 重发失败轮时挂回上一个干净轮次，让新轮次从失败发生之前继续。
 *
 * lastCleanRunId 来自对话记录：连续失败时，失败轮的 parentId 本身就是
 * 另一个失败轮，只回溯一级仍会继承失败事实，所以优先用它。
 */
export function agentTurnParentId(run: AgentRun | null | undefined, lastCleanRunId?: string) {
    if (!run?.id) return undefined;
    if (!isAgentRunTerminalForUser(run)) return run.id;
    // 只有在干净轮次确实更早时才采用它，避免脏数据把父轮指到未来的一轮。
    if (lastCleanRunId && lastCleanRunId !== run.id) return lastCleanRunId;
    return run.parentId || undefined;
}

/** 失败轮里最后一条用户消息就是被失败的请求；没有历史时退回输入框内容。 */
export function recoverableAgentPrompt(messages: RecoverableAgentMessage[], draft: string) {
    const lastUser = messages.findLast((item) => item.role === "user");
    return lastUser?.text?.trim() || draft.trim();
}
