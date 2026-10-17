import { afterAll, afterEach, beforeEach, describe, expect, it } from "bun:test";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { canvasContentHash } from "../src/lib/canvas/canvas-content";
import type { CanvasProject } from "../src/stores/canvas/use-canvas-store";
import type { CanvasOperationsRequest } from "../src/services/api/user-data";

// Run the production service and real three-way/hash algorithms. Only the
// account, network, media and persistence boundaries are replaced. A private
// module graph avoids mock.module leaking into other suites. No network is used.
const dir = mkdtempSync(join(tmpdir(), "canvas-sync-recovery-"));
const seamPath = join(dir, "boundaries.ts");
writeFileSync(seamPath, `
export const state: any = { scope: "user-a" };
const copy = (v: any) => JSON.parse(JSON.stringify(v));
export class ApiError extends Error {
    status: number; reason: string; retryable: boolean; retryAfterMs = 0;
    constructor(message: string, status = 503, reason = "network") {
        super(message); this.status = status; this.reason = reason; this.retryable = status >= 500;
    }
}
export const getActiveUserScope = () => state.scope;
export const localForageStorageForScope = (scope: string) => ({
    getItem: async (key: string) => { await state.storageHook?.("get", scope, key); return state.data.get(scope + ":" + key) ?? null; },
    setItem: async (key: string, value: string) => {
        state.writes.push({ kind: "set", scope, key, value });
        await state.storageHook?.("set", scope, key, value); state.data.set(scope + ":" + key, value);
    },
    removeItem: async (key: string) => {
        state.writes.push({ kind: "remove", scope, key });
        await state.storageHook?.("remove", scope, key); state.data.delete(scope + ":" + key);
    },
});
const store = (key: string) => ({
    getState: () => state[key],
    setState: (patch: any) => { state[key] = { ...state[key], ...(typeof patch === "function" ? patch(state[key]) : patch) }; },
    subscribe: () => () => {},
});
export const useCanvasStore = store("canvas");
export const useAssetStore = store("assets");
export const useSyncProgressStore = store("progress");
export const useCanvasHistoryStore = { getState: () => ({ clearHistory: () => {} }) };
export async function flushCanvasStorePersistence() {
    const scope = state.scope; const projects = copy(state.canvas.projects);
    state.flushes.push(scope); await state.flushHook?.(); state.durable.set(scope, projects);
}
export async function flushAssetStorePersistence() {}
export const getGenerationAssetDefaults = () => undefined;
export const appQueryClient = { invalidateQueries: async () => {} };
export const parseAssetRecordList = (v: any) => v;
export const assetForRemoteSync = (v: any) => v;
export const repairMissingCanvasAssets = () => {};
export const canvasNodeToAsset = () => undefined;
export const resourceIdFromStorageKey = () => null;
export const resourceFileUrl = (id: string) => "resource:" + id;
export const collectLocalMediaKeys = () => [];
export const numberValue = (value: any, fallback = 0) => Number.isFinite(value) ? value : fallback;
export const canvasEditLeases = { reset() {}, session: () => "test-session", versions: () => ({}), proofs: async () => [], acknowledged() {} };
export async function ensureRemoteResourceReferences(value: any) { await state.mediaHook?.(); return copy(value); }
export async function restoreRemoteTextNodeContents(value: any) { return value; }
const missing = () => { throw new Error("Unexpected non-canvas API call"); };
export const deleteRemoteAssets = missing, deleteRemoteCanvasProject = missing,
    getRemoteAsset = missing, getRemoteAssetsByIds = missing, listRemoteAssetsPage = missing, upsertRemoteAsset = missing;
function cloud(scope = state.scope) {
    let projects = state.cloud.get(scope); if (!projects) state.cloud.set(scope, projects = new Map()); return projects;
}
export async function getRemoteUserDataSnapshot() {
    const scope = state.scope; await state.snapshotHook?.();
    return { projects: copy([...cloud(scope).values()]), assets: [] };
}
export async function getRemoteCanvasProject(id: string) {
    const scope = state.scope; state.gets.push({ scope, id }); await state.getHook?.();
    const project = cloud(scope).get(id); if (!project) throw new ApiError("Not found", 404, "canvas_missing");
    return { project: copy(project) };
}
const eq = (a: any, b: any) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
function at(value: any, path: string) { return path.split(".").reduce((v, key) => v?.[key], value); }
function put(value: any, path: string, next: any) {
    const parts = path.split("."); const key = parts.pop()!;
    const parent = parts.reduce((v, part) => (v[part] ||= {}), value);
    if (next === null) delete parent[key]; else parent[key] = copy(next);
}
export async function applyRemoteCanvasOperations(id: string, input: any) {
    const scope = state.scope; const request = copy(input);
    state.patches.push({ scope, id, request });
    // A PATCH must never precede the durable request, including its retries.
    const journal = JSON.parse(state.data.get(scope + ":yingce:canvas-mutation-journal") || "[]");
    if (!journal.some((entry: any) => entry.projectId === id && eq(entry.request, request))) throw new Error("PATCH without durable original journal");
    const receiptKey = scope + ":" + id + ":" + request.mutationId;
    const receipt = state.receipts.get(receiptKey);
    if (receipt) {
        if (!eq(receipt.request, request)) throw new ApiError("Mutation payload changed", 409, "canvas_mutation_id_reused");
        return copy(receipt.response);
    }
    await state.patchHook?.();
    let project = copy(cloud(scope).get(id));
    const conflict = () => { throw new ApiError("Field conflict", 409, "canvas_field_conflict"); };
    for (const op of request.operations) {
        if (op.type === "set_field") { if (!eq(project[op.field], op.beforeValue)) conflict(); put(project, op.field, op.value); }
        else if (op.type === "create_node") { if (project.nodes.some((n: any) => n.id === op.node.id)) conflict(); project.nodes.push(op.node); }
        else if (op.type === "remove_node") {
            const node = project.nodes.find((n: any) => n.id === op.id); if (!node || !eq(node, op.expected)) conflict();
            project.nodes = project.nodes.filter((n: any) => n.id !== op.id);
        } else if (op.type === "patch_node") {
            const node = project.nodes.find((n: any) => n.id === op.id); if (!node) conflict();
            for (const path of Object.keys(op.patch)) { if (!eq(at(node, path), op.before[path])) conflict(); put(node, path, op.patch[path]); }
        } else throw new Error("Unhandled operation in regression fixture: " + op.type);
    }
    project.revision++; project.updatedAt = new Date().toISOString(); delete project.remoteContentHash;
    cloud(scope).set(id, project); state.applied++;
    const response = { project: { revision: project.revision }, mutationId: request.mutationId };
    state.receipts.set(receiptKey, { request, response });
    await state.afterPatchHook?.(); return copy(response);
}
export function resetBoundaries() {
    Object.assign(state, { scope: "user-a", data: new Map(), cloud: new Map(), receipts: new Map(), durable: new Map(),
        writes: [], flushes: [], patches: [], gets: [], applied: 0, storageHook: undefined, flushHook: undefined,
        getHook: undefined, patchHook: undefined, afterPatchHook: undefined, snapshotHook: undefined, mediaHook: undefined });
    state.canvas = { projects: [], openProject: (id: string) => state.canvas.projects.find((p: any) => p.id === id) ?? null,
        replaceProjects: (projects: any[]) => useCanvasStore.setState({ projects }) };
    state.assets = { assets: [], replaceAssets: (assets: any[]) => useAssetStore.setState({ assets }) };
    state.progress = { syncingProjects: {}, clearAll: () => { state.progress.syncingProjects = {}; },
        setProjectProgress: (id: string, patch: any) => { state.progress.syncingProjects[id] = { projectId: id, ...state.progress.syncingProjects[id], ...patch }; },
        incrementProjectCompleted: () => {} };
}
`);
const source = readFileSync(new URL("../src/services/user-data-sync.ts", import.meta.url), "utf8");
const realImports = new Map([
    ["json-canonicalize", fileURLToPath(import.meta.resolve("json-canonicalize"))],
    ["@/lib/canvas/canvas-content", fileURLToPath(new URL("../src/lib/canvas/canvas-content.ts", import.meta.url))],
    ["@/lib/canvas/agent-canvas-patch", fileURLToPath(new URL("../src/lib/canvas/agent-canvas-patch.ts", import.meta.url))],
    ["@/lib/canvas/canvas-task-mutation", fileURLToPath(new URL("../src/lib/canvas/canvas-task-mutation.ts", import.meta.url))],
    ["@/lib/asset-storage-revision", fileURLToPath(new URL("../src/lib/asset-storage-revision.ts", import.meta.url))],
    ["@/lib/secure-digest", fileURLToPath(new URL("../src/lib/secure-digest.ts", import.meta.url))],
]);
writeFileSync(join(dir, "sync.ts"), source.replace(/(from\s+)(["'])([^"']+)\2/g, (_match, prefix, _quote, specifier) => prefix + JSON.stringify(realImports.get(specifier) || seamPath)));
const sync: typeof import("../src/services/user-data-sync") = await import(join(dir, "sync.ts"));
const { state, ApiError, resetBoundaries } = await import(seamPath);
const journalKey = "yingce:canvas-mutation-journal";
const baselineKey = "yingce:canvas-confirmed-baseline:canvas";
const recoveryKey = "yingce:canvas-sync-recovery:canvas";
const copy = <T>(value: T): T => JSON.parse(JSON.stringify(value));
const nativeWindow = globalThis.window;
let timerId = 0;
let timers = new Map<number, { fn: () => void; delay: number }>();
beforeEach(() => {
    timers = new Map();
    globalThis.window = { setTimeout: (fn: () => void, delay: number) => { timers.set(++timerId, { fn, delay }); return timerId; }, clearTimeout: (id: number) => timers.delete(id) } as unknown as Window & typeof globalThis;
    resetBoundaries(); sync.resetRemoteUserDataSync();
});
afterEach(() => { sync.resetRemoteUserDataSync(); globalThis.window = nativeWindow; });
afterAll(() => rmSync(dir, { recursive: true, force: true }));
function canvas(patch: Partial<CanvasProject> = {}): CanvasProject {
    return { id: "canvas", title: "Canvas", revision: 1, createdAt: "2026-10-01", updatedAt: "2026-10-01",
        nodes: [node("A")], connections: [], chatSessions: [], activeChatId: null, backgroundMode: "dots", showImageInfo: false, viewport: { x: 0, y: 0, k: 1 }, previsScenes: [], ...patch } as CanvasProject;
}
function node(id: string, title = id): CanvasProject["nodes"][number] {
    return { id, type: "text", title, x: 0, y: 0, width: 200, height: 100, content: "Text " + id, metadata: {} } as CanvasProject["nodes"][number];
}
function local() { return state.canvas.openProject("canvas") as CanvasProject; }
function cloud() { return state.cloud.get(state.scope).get("canvas") as CanvasProject; }
function edit(patch: Partial<CanvasProject>) { state.canvas.projects = [{ ...local(), ...patch }]; }
function remoteEdit(patch: Partial<CanvasProject>) { state.cloud.get(state.scope).set("canvas", { ...cloud(), ...patch, revision: cloud().revision! + 1 }); }
function journal() { return JSON.parse(state.data.get(state.scope + ":" + journalKey) || "[]"); }
async function start(options: { local?: CanvasProject; remote?: CanvasProject; ancestor?: CanvasProject; unknown?: boolean } = {}) {
    const ancestor = options.ancestor || canvas();
    const known = { ...ancestor, remoteContentHash: await canvasContentHash(ancestor) };
    state.canvas.projects = [options.local ? { ...copy(options.local), remoteContentHash: options.unknown ? undefined : known.remoteContentHash } : known];
    state.durable.set(state.scope, copy(state.canvas.projects));
    state.cloud.set(state.scope, new Map([["canvas", copy(options.remote || ancestor)]]));
    if (!options.unknown) state.data.set(state.scope + ":" + baselineKey, JSON.stringify(known));
    await sync.initializeRemoteUserDataSession(state.scope);
}
function assertPreservedB() {
    expect(cloud().nodes.map((n) => n.id)).toContain("B");
    expect(local().nodes.map((n) => n.id)).toContain("B");
    expect(state.patches.flatMap((p: { request: CanvasOperationsRequest }) => p.request.operations).some((op: { type: string; id?: string }) => op.type === "remove_node" && op.id === "B")).toBe(false);
}
function failOnce(kind: string, key: string) {
    let failed = false;
    state.storageHook = async (operation: string, _scope: string, name: string) => {
        if (!failed && operation === kind && name === key) { failed = true; throw new Error("disk write failed"); }
    };
}
function deferred() {
    let resolve!: () => void; const promise = new Promise<void>((r) => { resolve = r; }); return { promise, resolve };
}

describe("production canvas sync recovery", () => {
    it.each(["load", "snapshot", "save", "agent"])("offline A edit and remote B addition survive %s", async (path) => {
        await start({ local: canvas({ nodes: [node("A", "Offline edit")] }), remote: canvas({ revision: 2, nodes: [node("A"), node("B")] }) });
        if (path === "load") await sync.loadCanvasProjectForEditing("canvas");
        if (path === "snapshot") await sync.syncRemoteUserData(state.scope);
        if (path === "agent") await sync.refreshCanvasAfterAgent("canvas");
        await sync.saveRemoteUserDataNow("canvas");
        expect(cloud().nodes.find((n) => n.id === "A")?.title).toBe("Offline edit"); assertPreservedB();
        expect(journal()).toEqual([]);
    });

    it("replays continued local editing on remote edits during PATCH, then saves only the new delta", async () => {
        await start({ ancestor: canvas({ nodes: [node("A"), node("B")] }) });
        edit({ nodes: [node("A", "First local edit"), node("B")] });
        state.afterPatchHook = async () => {
            state.afterPatchHook = undefined;
            edit({ nodes: [node("A", "Second local edit"), node("B")], viewport: { x: 9, y: 8, k: 2 } });
            remoteEdit({ nodes: [node("A", "First local edit"), node("B", "Remote B edit"), node("C")] });
        };
        await sync.saveRemoteUserDataNow("canvas");
        expect(cloud().nodes.map((n) => [n.id, n.title])).toEqual([["A", "Second local edit"], ["B", "Remote B edit"], ["C", "C"]]);
        expect(local().viewport).toEqual({ x: 9, y: 8, k: 2 });
        expect(state.patches).toHaveLength(2);
        expect(state.patches[1].request.operations).toEqual([{ type: "patch_node", id: "A", patch: { title: "Second local edit" }, before: { title: "First local edit" } }]);
    });

    it("PATCH success / GET failure retries the exact mutationId and request, applying only once", async () => {
        await start(); edit({ nodes: [node("A", "Saved edit")] });
        state.getHook = async () => { state.getHook = undefined; throw new ApiError("GET unavailable"); };
        await expect(sync.saveRemoteUserDataNow("canvas")).rejects.toThrow("GET unavailable");
        const pending = journal()[0]; expect(pending.request).toEqual(state.patches[0].request);
        expect([...timers.values()].some((t) => t.delay === 2000)).toBe(true);
        await sync.saveRemoteUserDataNow("canvas");
        expect(state.patches[1].request).toEqual(pending.request); expect(state.applied).toBe(1); expect(journal()).toEqual([]);
    });

    it.each(["canvas-flush", "baseline-write", "journal-clear"])("%s failure retains receipt and reconciles newer remote content on retry", async (failure) => {
        await start({ ancestor: canvas({ nodes: [node("A"), node("B")] }) });
        edit({ nodes: [node("A", "Saved edit"), node("B")] });
        if (failure === "canvas-flush") state.flushHook = async () => { state.flushHook = undefined; throw new Error("disk write failed"); };
        else failOnce(failure === "baseline-write" ? "set" : "remove", failure === "baseline-write" ? baselineKey : journalKey);
        await expect(sync.saveRemoteUserDataNow("canvas")).rejects.toThrow("disk write failed");
        const original = journal()[0].request; expect(original).toEqual(state.patches[0].request);
        remoteEdit({ nodes: [node("A", "Saved edit"), node("B", "Remote after failure")] });
        await sync.saveRemoteUserDataNow("canvas");
        expect(state.patches[1].request).toEqual(original); expect(state.applied).toBe(1);
        expect(local().nodes.find((n) => n.id === "B")?.title).toBe("Remote after failure"); expect(journal()).toEqual([]);
    });

    it.each(["old-cache", "projected-cache"])("reload with %s reuses the original receipt after failed persistence", async (cache) => {
        await start({ ancestor: canvas({ nodes: [node("A"), node("B")] }) });
        edit({ nodes: [node("A", "Saved edit"), node("B")] });
        state.durable.set(state.scope, copy(state.canvas.projects));
        if (cache === "old-cache") state.flushHook = async () => { state.flushHook = undefined; throw new Error("disk write failed"); };
        else failOnce("set", baselineKey);
        await expect(sync.saveRemoteUserDataNow("canvas")).rejects.toThrow();
        const original = journal()[0].request;
        state.canvas.projects = copy(state.durable.get(state.scope));
        await sync.initializeRemoteUserDataSession(state.scope);
        remoteEdit({ nodes: [node("A", "Saved edit"), node("B", "New remote B")] });
        await sync.saveRemoteUserDataNow("canvas");
        expect(state.patches[1].request).toEqual(original); expect(state.applied).toBe(1);
        expect(local().nodes.find((n) => n.id === "B")?.title).toBe("New remote B"); expect(journal()).toEqual([]);
    });

    it("a failed initial journal write forbids PATCH; retry durably records the same request first", async () => {
        await start(); edit({ nodes: [node("A", "Saved edit")] }); failOnce("set", journalKey);
        await expect(sync.saveRemoteUserDataNow("canvas")).rejects.toThrow("disk write failed"); expect(state.patches).toEqual([]);
        const original = JSON.parse(state.writes.find((w: { key: string }) => w.key === journalKey).value)[0].request;
        await sync.saveRemoteUserDataNow("canvas");
        expect(state.patches[0].request).toEqual(original); expect(journal()).toEqual([]);
    });

    it("missing ancestry preserves a recovery copy and pauses instead of guessing deletion", async () => {
        await start({ local: canvas({ nodes: [node("A", "Unproven edit")] }), remote: canvas({ nodes: [node("A"), node("B")] }), unknown: true });
        const original = copy(local()); await sync.loadCanvasProjectForEditing("canvas");
        await expect(sync.saveRemoteUserDataNow("canvas")).rejects.toThrow("暂停");
        expect(state.patches).toEqual([]); expect(local()).toEqual(original);
        expect(JSON.parse(state.data.get(state.scope + ":" + recoveryKey)).original).toEqual(original);
    });

    it.each(["{broken", "{}", '[{"projectId":"canvas"}]'])("corrupt journal %s is retained byte-for-byte and cannot trigger PATCH", async (raw) => {
        state.data.set(state.scope + ":" + journalKey, raw); state.canvas.projects = [canvas()];
        await expect(sync.initializeRemoteUserDataSession(state.scope)).rejects.toThrow();
        expect(state.data.get(state.scope + ":" + journalKey)).toBe(raw); expect(state.patches).toEqual([]);
    });

    it("a queued old-account save and restore stop after the serialized switch", async () => {
        await start(); edit({ nodes: [node("A", "Account A draft")] });
        const blocker = deferred(); const entered = deferred();
        const hold = sync.withRemoteUserDataSyncExclusive(async () => { entered.resolve(); await blocker.promise; }); await entered.promise;
        const switchAccount = sync.withRemoteUserDataSyncExclusive(async () => { sync.resetRemoteUserDataSync(); state.scope = "user-b"; state.canvas.projects = [canvas({ title: "B private" })]; });
        const save = sync.saveRemoteUserDataNow("canvas").catch((e) => e);
        const restore = sync.restoreRemoteCanvasProject({ projectId: "canvas", expectedRevision: 1, expectedScope: "user-a", document: canvas() }).catch((e) => e);
        blocker.resolve(); await hold; await switchAccount;
        expect(await save).toBeInstanceOf(Error); expect(await restore).toBeInstanceOf(Error);
        expect(state.patches).toEqual([]); expect(state.writes).toEqual([]); expect(local().title).toBe("B private");
    });

    it("an in-flight old-account receipt cannot publish or persist into the new account", async () => {
        await start(); edit({ nodes: [node("A", "Account A draft")] });
        state.afterPatchHook = async () => { sync.resetRemoteUserDataSync(); state.scope = "user-b"; state.canvas.projects = [canvas({ title: "B private" })]; };
        await expect(sync.saveRemoteUserDataNow("canvas")).rejects.toThrow("账号");
        expect(state.patches.map((p: { scope: string }) => p.scope)).toEqual(["user-a"]); expect(state.gets).toEqual([]);
        expect(state.writes.every((w: { scope: string }) => w.scope === "user-a")).toBe(true);
        expect(state.data.has("user-a:" + journalKey)).toBe(true); expect(local().title).toBe("B private");
    });

    it("an account B session cannot replay account A's durable mutation", async () => {
        await start(); edit({ nodes: [node("A", "A pending")] });
        state.getHook = async () => { state.getHook = undefined; throw new ApiError("GET unavailable"); };
        await expect(sync.saveRemoteUserDataNow("canvas")).rejects.toThrow(); const oldJournal = state.data.get("user-a:" + journalKey);
        state.scope = "user-b"; await start(); await sync.saveRemoteUserDataNow("canvas");
        expect(state.patches).toHaveLength(1); expect(state.data.get("user-a:" + journalKey)).toBe(oldJournal); expect(local().title).toBe("Canvas");
    });

    it("manual recovery backs up the paused draft, uses operations and releases the pause only on success", async () => {
        await start({ local: canvas({ nodes: [node("A", "Unproven draft")] }), remote: canvas({ nodes: [node("A"), node("B")] }), unknown: true });
        const oldDraft = copy(local());
        const restored = await sync.restoreRemoteCanvasProject({ projectId: "canvas", expectedRevision: 1, expectedScope: state.scope, document: canvas({ nodes: [node("A", "Chosen history")] }) });
        expect(restored.nodes.map((n) => n.title)).toEqual(["Chosen history"]); expect(state.patches[0].request.baseRevision).toBe(1);
        expect(state.patches[0].request.operations.some((op: { type: string }) => op.type === "remove_node")).toBe(true);
        expect(JSON.parse(state.data.get(state.scope + ":" + recoveryKey)).original).toEqual(oldDraft);
        edit({ title: "After recovery" }); await sync.saveRemoteUserDataNow("canvas"); expect(cloud().title).toBe("After recovery");
    });

    it.each(["revision", "scope", "backup"])("manual recovery rejects %s mismatch/failure before PATCH", async (failure) => {
        await start(); const original = copy(local());
        if (failure === "backup") failOnce("set", recoveryKey);
        await expect(sync.restoreRemoteCanvasProject({ projectId: "canvas", expectedRevision: failure === "revision" ? 2 : 1, expectedScope: failure === "scope" ? "user-b" : state.scope, document: canvas({ title: "Chosen history" }) })).rejects.toThrow();
        expect(local()).toEqual(original); expect(state.patches).toEqual([]);
    });

    it.each(["GET", "baseline", "clear"])("manual restore %s failure reuses the original restore mutation, without a second restore", async (failure) => {
        await start();
        if (failure === "GET") state.afterPatchHook = async () => { state.afterPatchHook = undefined; state.getHook = async () => { state.getHook = undefined; throw new ApiError("GET unavailable"); }; };
        else failOnce(failure === "baseline" ? "set" : "remove", failure === "baseline" ? baselineKey : journalKey);
        const input = { projectId: "canvas", expectedRevision: 1, expectedScope: state.scope, document: canvas({ title: "Chosen history" }) };
        await expect(sync.restoreRemoteCanvasProject(input)).rejects.toThrow();
        const original = journal()[0].request; const restored = await sync.restoreRemoteCanvasProject(input);
        expect(restored.title).toBe("Chosen history"); expect(state.patches[1].request).toEqual(original); expect(state.applied).toBe(1); expect(journal()).toEqual([]);
    });

    it("pending receipt coordination followed by stale manual restore must preserve continued local edits", async () => {
        await start(); edit({ nodes: [node("A", "First edit")] });
        state.getHook = async () => { state.getHook = undefined; throw new ApiError("GET unavailable"); };
        await expect(sync.saveRemoteUserDataNow("canvas")).rejects.toThrow(); edit({ nodes: [node("A", "Continued edit")] });
        await expect(sync.restoreRemoteCanvasProject({ projectId: "canvas", expectedRevision: 1, expectedScope: state.scope, document: canvas({ title: "Chosen history" }) })).rejects.toThrow("版本");
        expect(local().nodes[0].title).toBe("Continued edit"); expect(state.applied).toBe(1);
    });
});
