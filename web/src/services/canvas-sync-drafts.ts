import { nanoid } from "nanoid";
import { localForageStorageForScope } from "@/lib/localforage-storage";
import { getActiveUserScope, isUserScopedPersistenceSuppressed } from "@/lib/user-scope";
import { sameCanvasContent } from "@/lib/canvas/canvas-content";
import { withCanvasStorePersistenceLock, type CanvasProject } from "@/stores/canvas/use-canvas-store";
import { withGenerationArtifactCommitLock } from "@/services/generation-asset-repository";
import { useSyncProgressStore } from "@/stores/use-sync-progress-store";
import type { CanvasExportFile } from "@/types/canvas-export";

export type CanvasSyncDraft = {
    id: string;
    savedAt: string;
    project: CanvasProject;
    source?: "legacy" | "recovery-original" | "recovery-latest";
    message?: string;
};
export type CanvasDraftReadIssue = { key: string; message: string; raw?: string };
export type CanvasSyncDraftInventory = { drafts: CanvasSyncDraft[]; issues: CanvasDraftReadIssue[] };
const draftKey = (projectId: string) => `yingce:sync-drafts:${projectId}`;
const recoveryKey = (projectId: string) => `yingce:canvas-sync-recovery:${projectId}`;
const draftIndexKey = "yingce:sync-draft-index";

function requireScope(scope: string) {
    if (getActiveUserScope() !== scope) throw new Error("账号已切换，已停止读取或保存旧账号草稿");
}

function isProject(value: unknown, projectId: string): value is CanvasProject {
    if (!value || typeof value !== "object") return false;
    const project = value as CanvasProject;
    return project.id === projectId && typeof project.title === "string"
        && Array.isArray(project.nodes) && project.nodes.every((node) => node && typeof node.id === "string" && typeof node.type === "string" && typeof node.title === "string" && Number.isFinite(node.position?.x) && Number.isFinite(node.position?.y) && Number.isFinite(node.width) && Number.isFinite(node.height))
        && Array.isArray(project.connections) && project.connections.every((connection) => connection && typeof connection.id === "string" && typeof connection.fromNodeId === "string" && typeof connection.toNodeId === "string")
        && Boolean(project.viewport) && Number.isFinite(project.viewport.x) && Number.isFinite(project.viewport.y) && Number.isFinite(project.viewport.k) && project.viewport.k > 0;
}

function parseLegacyDrafts(raw: string | null, projectId: string): CanvasSyncDraft[] {
    if (!raw) return [];
    const drafts: unknown = JSON.parse(raw);
    if (!Array.isArray(drafts) || drafts.some((draft) => !draft || typeof draft.id !== "string" || typeof draft.savedAt !== "string" || !isProject(draft.project, projectId))) throw new Error("旧草稿记录损坏，原始数据已保留，请导出后检查");
    return drafts;
}

/** Parse independently: damaged legacy bytes must not hide a valid recovery copy. */
export function parseCanvasSyncDraftInventory(projectId: string, raw: { legacy: string | null; recovery: string | null; unreadable?: string | null }): CanvasSyncDraftInventory {
    const drafts: CanvasSyncDraft[] = [];
    const issues: CanvasDraftReadIssue[] = [];
    try {
        drafts.push(...parseLegacyDrafts(raw.legacy, projectId).map((draft) => ({ ...draft, source: "legacy" as const })));
    } catch (error) {
        issues.push({ key: draftKey(projectId), message: error instanceof Error ? error.message : "旧草稿读取失败", raw: raw.legacy || undefined });
    }
    if (raw.recovery) {
        try {
            const saved = JSON.parse(raw.recovery) as { original?: unknown; latest?: unknown; savedAt?: string; message?: string };
            if (!saved || typeof saved !== "object") throw new Error("恢复副本格式无效");
            for (const name of ["original", "latest"] as const) {
                const project = saved[name];
                if (!isProject(project, projectId)) {
                    issues.push({ key: recoveryKey(projectId), message: `恢复副本的 ${name} 内容无效，原始数据仍保留`, raw: raw.recovery });
                    continue;
                }
                // Do not deduplicate against legacy drafts: each source is separately discoverable.
                if (name === "latest" && drafts.some((draft) => draft.source === "recovery-original" && sameCanvasContent(draft.project, project))) continue;
                drafts.push({ id: `recovery:${name}:${projectId}`, savedAt: typeof saved.savedAt === "string" ? saved.savedAt : project.updatedAt, project, source: name === "original" ? "recovery-original" : "recovery-latest", message: typeof saved.message === "string" ? saved.message : undefined });
            }
        } catch (error) {
            issues.push({ key: recoveryKey(projectId), message: error instanceof Error ? error.message : "恢复副本读取失败", raw: raw.recovery });
        }
    }
    if (raw.unreadable) issues.push({ key: recoveryKey(projectId) + ":unreadable", message: "还有一份无法解析的旧恢复记录，可导出原始数据", raw: raw.unreadable });
    return { drafts, issues };
}

export async function readCanvasSyncDraftInventory(projectId: string, scope = getActiveUserScope()): Promise<CanvasSyncDraftInventory> {
    requireScope(scope);
    const storage = localForageStorageForScope(scope);
    const keys = [draftKey(projectId), recoveryKey(projectId), recoveryKey(projectId) + ":unreadable"];
    const results = await Promise.all(keys.map(async (key) => {
        try { return { key, raw: await storage.getItem(key) }; }
        catch { return { key, raw: null, error: "本地存储读取失败，请刷新重试" }; }
    }));
    requireScope(scope);
    const inventory = parseCanvasSyncDraftInventory(projectId, { legacy: results[0].raw, recovery: results[1].raw, unreadable: results[2].raw });
    inventory.issues.push(...results.filter((result) => result.error).map((result) => ({ key: result.key, message: result.error! })));
    return inventory;
}

export async function readCanvasSyncDrafts(projectId: string, scope = getActiveUserScope()): Promise<CanvasSyncDraft[]> {
    const inventory = await readCanvasSyncDraftInventory(projectId, scope);
    if (inventory.issues.length) throw new Error(inventory.issues.map((issue) => issue.message).join("；"));
    return inventory.drafts;
}

export async function readAllCanvasSyncDrafts(scope = getActiveUserScope()) {
    requireScope(scope);
    const raw = await localForageStorageForScope(scope).getItem(draftIndexKey);
    requireScope(scope);
    const ids: unknown = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(ids) || ids.some((id) => typeof id !== "string")) throw new Error("旧草稿索引损坏，已保留原始数据");
    const drafts = (await Promise.all(ids.map((id) => readCanvasSyncDrafts(id, scope)))).flat();
    requireScope(scope);
    return drafts;
}

/** Caller holds the scope's canvas persistence lock. Never reacquire it here. */
export async function preserveCanvasSyncDraftLocked(snapshot: CanvasProject, scope: string) {
    try {
        requireScope(scope);
        if (isUserScopedPersistenceSuppressed()) throw new Error("账号数据正在切换，已停止草稿保存");
        const storage = localForageStorageForScope(scope);
        const raw = await storage.getItem(draftKey(snapshot.id));
        requireScope(scope);
        const drafts = parseLegacyDrafts(raw, snapshot.id);
        if (!drafts.some((draft) => sameCanvasContent(draft.project, snapshot))) {
            const rawIndex = await storage.getItem(draftIndexKey);
            requireScope(scope);
            const ids: unknown = rawIndex ? JSON.parse(rawIndex) : [];
            if (!Array.isArray(ids) || ids.some((id) => typeof id !== "string")) throw new Error("旧草稿索引损坏，已停止覆盖");
            // Index first: failure saving the document leaves a harmless empty index entry,
            // never an undiscoverable draft. No recovery/journal data is rewritten here.
            if (!ids.includes(snapshot.id)) {
                requireScope(scope);
                await storage.setItem(draftIndexKey, JSON.stringify([...ids, snapshot.id]));
                requireScope(scope);
            }
            drafts.push({ id: nanoid(), savedAt: new Date().toISOString(), project: structuredClone(snapshot) });
            const serialized = JSON.stringify(drafts);
            requireScope(scope);
            await storage.setItem(draftKey(snapshot.id), serialized);
            requireScope(scope);
            if (await storage.getItem(draftKey(snapshot.id)) !== serialized) throw new Error("本地草稿未能持久保存，已停止版本恢复");
            requireScope(scope);
        }
        useSyncProgressStore.getState().setProjectProgress(snapshot.id, { draftCount: drafts.length, draftError: undefined });
        return drafts.length;
    } catch (error) {
        if (getActiveUserScope() === scope) useSyncProgressStore.getState().setProjectProgress(snapshot.id, { phase: "error", errorKind: "draft", draftError: error instanceof Error ? error.message : "本地草稿保存失败", message: "草稿保存失败，当前编辑内容保持不变" });
        throw error;
    }
}

export async function preserveCanvasSyncDraft(project: CanvasProject, scope = getActiveUserScope()) {
    requireScope(scope);
    const snapshot = structuredClone(project);
    return withGenerationArtifactCommitLock(scope, () => withCanvasStorePersistenceLock(scope, () => preserveCanvasSyncDraftLocked(snapshot, scope)));
}

/** Content-only backup, importable as a new canvas; never reads live media/drawing caches. */
export function canvasVersionExportDocument(project: CanvasProject): CanvasExportFile {
    return { app: "yingce", version: 4, exportedAt: new Date().toISOString(), projects: [{ project: structuredClone(project), files: [] }] };
}
