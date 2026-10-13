import { afterEach, beforeEach, expect, spyOn, test } from "bun:test";
import localforage from "localforage";
import { strFromU8, strToU8, unzipSync, zipSync } from "fflate";
import { scopedStorageKey, setActiveUserScope } from "@/lib/user-scope";
import { canvasVersionExportDocument, parseCanvasSyncDraftInventory, preserveCanvasSyncDraft, preserveCanvasSyncDraftLocked, readAllCanvasSyncDrafts, readCanvasSyncDraftInventory } from "@/services/canvas-sync-drafts";
import { useSyncProgressStore } from "@/stores/use-sync-progress-store";
import type { CanvasProject } from "@/stores/canvas/use-canvas-store";
import { CanvasNodeType } from "@/types/canvas";

const originalWindow = globalThis.window;
const data = new Map<string, string>();
const session = new Map<string, string>();
const draftKey = "yingce:sync-drafts:canvas";
const indexKey = "yingce:sync-draft-index";
const recoveryKey = "yingce:canvas-sync-recovery:canvas";
const userKey = (key: string, scope = "user-a") => scopedStorageKey(key, scope);
let get: ReturnType<typeof spyOn>;
let set: ReturnType<typeof spyOn>;
let remove: ReturnType<typeof spyOn>;

beforeEach(() => {
    data.clear(); session.clear();
    globalThis.window = {
        localStorage: { getItem: (key: string) => session.get(key) ?? null, setItem: (key: string, value: string) => session.set(key, value), removeItem: (key: string) => session.delete(key) },
    } as unknown as Window & typeof globalThis;
    setActiveUserScope("user-a");
    get = spyOn(localforage, "getItem").mockImplementation(async (key: string) => data.get(key) ?? null);
    set = spyOn(localforage, "setItem").mockImplementation(async (key: string, value: string) => { data.set(key, value); return value; });
    remove = spyOn(localforage, "removeItem").mockImplementation(async () => undefined);
    useSyncProgressStore.getState().clearAll();
});
afterEach(() => {
    get.mockRestore(); set.mockRestore(); remove.mockRestore();
    globalThis.window = originalWindow;
    useSyncProgressStore.getState().clearAll();
});

function project(title = "Canvas"): CanvasProject {
    return { id: "canvas", title, revision: 8, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-13T00:00:00Z",
        nodes: [{ id: "image", type: CanvasNodeType.Image, title: "Image", position: { x: 0, y: 0 }, width: 100, height: 100, metadata: { storageKey: "resource:fixture", content: "https://example.invalid/image.png" } }],
        connections: [], chatSessions: [], activeChatId: null, backgroundMode: "dots", showImageInfo: false, viewport: { x: 0, y: 0, k: 1 }, previsScenes: [] };
}
const legacy = (value = project()) => JSON.stringify([{ id: "old", savedAt: value.updatedAt, project: value }]);
const recovery = (original = project(), latest = project("Continued edits")) => JSON.stringify({ original, latest, savedAt: original.updatedAt, message: "Paused for conflict", mutations: [{ mutationId: "retained" }] });

test("legacy yingce draft key and index remain readable under the original user scope", async () => {
    data.set(userKey(draftKey), legacy());
    data.set(userKey(indexKey), JSON.stringify(["canvas"]));
    data.set(userKey(draftKey, "user-b"), legacy(project("Other account")));
    const inventory = await readCanvasSyncDraftInventory("canvas", "user-a");
    expect(inventory.issues).toEqual([]);
    expect(inventory.drafts[0]).toMatchObject({ id: "old", source: "legacy", project: { title: "Canvas" } });
    expect(await readAllCanvasSyncDrafts("user-a")).toEqual(inventory.drafts);
    expect(set).not.toHaveBeenCalled(); expect(remove).not.toHaveBeenCalled();
});

test("paused recovery exposes original and continued edits without mutating journal bytes", async () => {
    const raw = recovery();
    data.set(userKey(recoveryKey), raw);
    const inventory = await readCanvasSyncDraftInventory("canvas");
    expect(inventory.drafts.map((draft) => draft.source)).toEqual(["recovery-original", "recovery-latest"]);
    expect(inventory.drafts[1].project.title).toBe("Continued edits");
    expect(data.get(userKey(recoveryKey))).toBe(raw);
    expect(set).not.toHaveBeenCalled(); expect(remove).not.toHaveBeenCalled();
});

test("identical recovery copies deduplicate only within recovery, not against legacy", () => {
    const copy = project(); copy.revision = 9; copy.viewport.x = 150;
    const inventory = parseCanvasSyncDraftInventory("canvas", { legacy: legacy(), recovery: recovery(project(), copy) });
    expect(inventory.drafts.map((draft) => draft.source)).toEqual(["legacy", "recovery-original"]);
});

test("broken legacy remains exportable and does not hide valid recovery", () => {
    const inventory = parseCanvasSyncDraftInventory("canvas", { legacy: "{broken", recovery: recovery() });
    expect(inventory.drafts).toHaveLength(2);
    expect(inventory.issues).toMatchObject([{ key: draftKey, raw: "{broken" }]);
});

test("broken recovery and quarantined bytes remain exportable without hiding legacy", () => {
    const inventory = parseCanvasSyncDraftInventory("canvas", { legacy: legacy(), recovery: "{broken", unreadable: "original bytes" });
    expect(inventory.drafts).toHaveLength(1);
    expect(inventory.issues.map((issue) => issue.raw)).toEqual(["{broken", "original bytes"]);
});

test("wrong project or invalid viewport cannot be previewed; raw bytes survive", () => {
    const invalid = project(); invalid.viewport.k = 0;
    const wrong = project(); wrong.id = "different";
    const raw = recovery(wrong, invalid);
    const inventory = parseCanvasSyncDraftInventory("canvas", { legacy: null, recovery: raw });
    expect(inventory.drafts).toEqual([]);
    expect(inventory.issues).toHaveLength(2);
    expect(inventory.issues.every((issue) => issue.raw === raw)).toBe(true);
});

test("scope switch during a read suppresses results and causes no writes or deletes", async () => {
    get.mockImplementation(async () => { setActiveUserScope("user-b"); return legacy(); });
    await expect(readCanvasSyncDraftInventory("canvas", "user-a")).rejects.toThrow("账号已切换");
    expect(set).not.toHaveBeenCalled(); expect(remove).not.toHaveBeenCalled();
});

test("scope switch during draft preflight prevents all old-account writes", async () => {
    get.mockImplementation(async () => { setActiveUserScope("user-b"); return null; });
    await expect(preserveCanvasSyncDraftLocked(project(), "user-a")).rejects.toThrow("账号已切换");
    expect(set).not.toHaveBeenCalled(); expect(remove).not.toHaveBeenCalled();
    expect(useSyncProgressStore.getState().syncingProjects).toEqual({});
});

test("damaged legacy or index prevents overwrites and reports a durable-draft failure", async () => {
    data.set(userKey(draftKey), "raw damaged draft");
    await expect(preserveCanvasSyncDraftLocked(project(), "user-a")).rejects.toThrow();
    expect(data.get(userKey(draftKey))).toBe("raw damaged draft");
    expect(set).not.toHaveBeenCalled();
    data.delete(userKey(draftKey)); data.set(userKey(indexKey), "raw damaged index");
    await expect(preserveCanvasSyncDraftLocked(project(), "user-a")).rejects.toThrow();
    expect(data.get(userKey(indexKey))).toBe("raw damaged index");
    expect(set).not.toHaveBeenCalled();
    expect(useSyncProgressStore.getState().syncingProjects.canvas.errorKind).toBe("draft");
});

test("draft preservation uses the existing locks and never rewrites recovery or mutations", async () => {
    const raw = recovery(); data.set(userKey(recoveryKey), raw);
    const snapshot = project();
    expect(await preserveCanvasSyncDraft(snapshot, "user-a")).toBe(1);
    snapshot.title = "Later edit";
    expect(JSON.parse(data.get(userKey(draftKey))!)[0].project.title).toBe("Canvas");
    expect(JSON.parse(data.get(userKey(indexKey))!)).toEqual(["canvas"]);
    expect(set.mock.calls.map((args) => args[0])).toEqual([userKey(indexKey), userKey(draftKey)]);
    expect(data.get(userKey(recoveryKey))).toBe(raw); expect(remove).not.toHaveBeenCalled();
});

test("an already-preserved content version does not append or rewrite it", async () => {
    const raw = legacy(); data.set(userKey(draftKey), raw);
    const copy = project(); copy.revision++; copy.viewport.y = 10;
    expect(await preserveCanvasSyncDraftLocked(copy, "user-a")).toBe(1);
    expect(data.get(userKey(draftKey))).toBe(raw); expect(set).not.toHaveBeenCalled();
});

test("silent storage failure is not reported as a successful draft backup", async () => {
    set.mockImplementation(async (_key: string, value: string) => value);
    await expect(preserveCanvasSyncDraftLocked(project(), "user-a")).rejects.toThrow("持久保存");
    expect(useSyncProgressStore.getState().syncingProjects.canvas.phase).toBe("error");
});

test("content export clones the selected version in the existing projects.json ZIP format", () => {
    const selected = project();
    const document = canvasVersionExportDocument(selected);
    document.projects[0].project.title = "Export copy";
    expect(selected.title).toBe("Canvas");
    const archive = unzipSync(zipSync({ "projects.json": strToU8(JSON.stringify(document)) }));
    const imported = JSON.parse(strFromU8(archive["projects.json"]));
    expect(imported).toMatchObject({ app: "yingce", version: 4, projects: [{ project: { title: "Export copy" }, files: [] }] });
    expect(imported.projects[0].project.nodes[0].metadata).toEqual(selected.nodes[0].metadata);
    expect(imported.projects[0].drawingDocuments).toBeUndefined();
});
