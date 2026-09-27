import { afterAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const tempDir = mkdtempSync(join(tmpdir(), "canvas-file-storage-upload-"));
const modulePaths = {
    localforage: join(tempDir, "localforage.ts"),
    nanoid: join(tempDir, "nanoid.ts"),
    scope: join(tempDir, "scope.ts"),
    poster: join(tempDir, "poster.ts"),
    resources: join(tempDir, "resources.ts"),
    imageStorage: join(tempDir, "image-storage.ts"),
    resourceBlobCache: join(tempDir, "resource-blob-cache.ts"),
    fileStorage: join(tempDir, "file-storage.ts"),
};

writeFileSync(modulePaths.localforage, `
export const state = { data: new Map(), writes: [], removals: [] };
export default {
    createInstance(options = {}) {
        const storeName = options.storeName || "";
        return {
            async setItem(key, value) { state.writes.push({ storeName, key, value }); state.data.set(storeName + ":" + key, value); return value; },
            async removeItem(key) { state.removals.push({ storeName, key }); state.data.delete(storeName + ":" + key); },
            async getItem(key) { return state.data.get(storeName + ":" + key) ?? null; },
            async iterate() {},
        };
    },
};
`);
writeFileSync(modulePaths.nanoid, `let sequence = 0; export function nanoid() { sequence += 1; return "upload-" + sequence; }`);
writeFileSync(modulePaths.scope, `export function getActiveUserScope() { return "cancel-test-user"; }`);
writeFileSync(modulePaths.poster, `
export const state = { captureCalls: [], detectCalls: 0, captureResult: { width: 1920, height: 1080, durationMs: 1200, hasAudio: true, poster: new Blob(["poster"], { type: "image/jpeg" }) } };
export async function captureVideoPoster(_source, options = {}) { state.captureCalls.push(options); return state.captureResult; }
export async function detectVideoAudioTrackFromBlob() { state.detectCalls += 1; return true; }
`);
writeFileSync(modulePaths.resources, `
export class ResourceUploadError extends Error {
    constructor(message, options = {}) { super(message); this.name = "ResourceUploadError"; this.permanent = Boolean(options.permanent); }
}
export const state = {
    calls: [],
    error: undefined,
    abortController: undefined,
    abortReason: undefined,
    resource: { id: "resource-video", size: 5, mimeType: "video/mp4", width: 1920, height: 1080, durationMs: 1200 },
};
export async function uploadResourceFile(file, kind, meta, onProgress, signal) {
    state.calls.push({ file, kind, meta, onProgress, signal });
    if (state.abortController && state.abortReason !== undefined) state.abortController.abort(state.abortReason);
    if (state.error !== undefined) throw state.error;
    return state.resource;
}
export function resourceStorageKey(id) { return "resource:" + id; }
export function resourceFileUrl(id) { return "/resources/" + id + "/file"; }
export function resourceIdFromStorageKey(key) { return key.startsWith("resource:") ? key.slice(9) : ""; }
export async function getResourceAccess() { throw new Error("unexpected getResourceAccess"); }
export function resolveResourceAccessURL(url) { return url; }
`);
writeFileSync(modulePaths.imageStorage, `
export const state = { calls: [], error: undefined };
export async function uploadImage(input, onProgress, signal) {
    state.calls.push({ input, onProgress, signal });
    if (state.error !== undefined) throw state.error;
    return { url: "blob:poster", storageKey: "image:poster", width: 320, height: 180, bytes: 6, mimeType: "image/jpeg" };
}
`);
writeFileSync(modulePaths.resourceBlobCache, `
export async function getCachedResourceBlob() { return null; }
export async function primeResourceBlobCache() { return "blob:resource"; }
`);

const source = readFileSync(new URL("../src/services/file-storage.ts", import.meta.url), "utf8")
    .replaceAll('"localforage"', JSON.stringify(pathToFileURL(modulePaths.localforage).href))
    .replaceAll('"nanoid"', JSON.stringify(pathToFileURL(modulePaths.nanoid).href))
    .replaceAll('"@/lib/user-scope"', JSON.stringify(pathToFileURL(modulePaths.scope).href))
    .replaceAll('"@/lib/video-poster"', JSON.stringify(pathToFileURL(modulePaths.poster).href))
    .replaceAll('"@/services/api/resources"', JSON.stringify(pathToFileURL(modulePaths.resources).href))
    .replaceAll('"@/services/image-storage"', JSON.stringify(pathToFileURL(modulePaths.imageStorage).href))
    .replaceAll('"@/services/resource-blob-cache"', JSON.stringify(pathToFileURL(modulePaths.resourceBlobCache).href));
writeFileSync(modulePaths.fileStorage, source);

const fileStorage: typeof import("../src/services/file-storage") = await import(`${pathToFileURL(modulePaths.fileStorage).href}?upload-cancel`);
const storageState: { data: Map<string, unknown>; writes: Array<{ storeName: string; key: string; value: unknown }>; removals: Array<{ storeName: string; key: string }> } = (await import(pathToFileURL(modulePaths.localforage).href)).state;
const posterState: { captureCalls: Array<{ signal?: AbortSignal }>; detectCalls: number; captureResult: unknown } = (await import(pathToFileURL(modulePaths.poster).href)).state;
const resourceState: { calls: Array<{ kind: string; signal?: AbortSignal }>; error?: unknown; abortController?: AbortController; abortReason?: unknown; resource: Record<string, unknown> } = (await import(pathToFileURL(modulePaths.resources).href)).state;
const imageState: { calls: Array<{ signal?: AbortSignal }>; error?: unknown } = (await import(pathToFileURL(modulePaths.imageStorage).href)).state;

const hookSource = readFileSync(new URL("../src/pages/canvas/use-canvas-upload.ts", import.meta.url), "utf8");
const originalDocument = globalThis.document;

beforeEach(() => {
    storageState.data.clear();
    storageState.writes.length = 0;
    storageState.removals.length = 0;
    posterState.captureCalls.length = 0;
    posterState.detectCalls = 0;
    posterState.captureResult = { width: 1920, height: 1080, durationMs: 1200, poster: new Blob(["poster"], { type: "image/jpeg" }) };
    resourceState.calls.length = 0;
    resourceState.error = undefined;
    resourceState.abortController = undefined;
    resourceState.abortReason = undefined;
    imageState.calls.length = 0;
    imageState.error = undefined;
});

afterAll(() => {
    if (originalDocument === undefined) delete (globalThis as { document?: unknown }).document;
    else Object.defineProperty(globalThis, "document", { configurable: true, value: originalDocument });
    rmSync(tempDir, { recursive: true, force: true });
});

describe("媒体上传取消信号", () => {
    test("视频元数据、poster 和资源上传共用同一个 signal", async () => {
        const controller = new AbortController();
        const result = await fileStorage.uploadMediaFile(new Blob(["video"], { type: "video/mp4" }), "video", undefined, controller.signal);

        expect(posterState.captureCalls[0]?.signal).toBe(controller.signal);
        expect(posterState.detectCalls).toBe(1);
        expect(imageState.calls[0]?.signal).toBe(controller.signal);
        expect(resourceState.calls[0]?.kind).toBe("video");
        expect(resourceState.calls[0]?.signal).toBe(controller.signal);
        expect(result.pendingRemoteUpload).toBeUndefined();
    });

    test("file 上传取消原样抛出 AbortError，且不写入 pendingRemoteUpload", async () => {
        const controller = new AbortController();
        const abortError = new DOMException("用户取消", "AbortError");
        resourceState.error = new Error("transport interrupted");
        resourceState.abortController = controller;
        resourceState.abortReason = abortError;

        await expect(fileStorage.uploadMediaFile(new Blob(["file"], { type: "application/octet-stream" }), "file", undefined, controller.signal)).rejects.toBe(abortError);
        expect(resourceState.calls[0]?.signal).toBe(controller.signal);
        expect(storageState.writes.filter((item) => item.storeName === "media_files")).toHaveLength(0);
    });

    test("音频元数据阶段取消不会继续上传或落本地 pending", async () => {
        const previousDocument = globalThis.document;
        const audio = {
            duration: 3,
            onloadedmetadata: null as (() => void) | null,
            onerror: null as (() => void) | null,
            src: "",
            removeAttribute: () => undefined,
            load: () => undefined,
        };
        Object.defineProperty(globalThis, "document", {
            configurable: true,
            value: { createElement: (tag: string) => tag === "audio" ? audio : {} },
        });
        try {
            const controller = new AbortController();
            const abortError = new DOMException("停止音频上传", "AbortError");
            const pending = fileStorage.uploadMediaFile(new Blob(["audio"], { type: "audio/mpeg" }), "audio", undefined, controller.signal);
            await Promise.resolve();
            controller.abort(abortError);

            await expect(pending).rejects.toBe(abortError);
            expect(resourceState.calls).toHaveLength(0);
            expect(storageState.writes.filter((item) => item.storeName === "media_files")).toHaveLength(0);
        } finally {
            if (previousDocument === undefined) delete (globalThis as { document?: unknown }).document;
            else Object.defineProperty(globalThis, "document", { configurable: true, value: previousDocument });
        }
    });
});

test("画布文件上传为每个活动任务建立 controller，并将 signal 传到图片、文件和媒体路径", () => {
    expect(hookSource).toContain("const activeUploadsRef = useRef(new Map<string, AbortController>());");
    expect(hookSource).toContain("activeUploadsRef.current.forEach((controller) => controller.abort());");
    expect(hookSource).toContain("activeUploadsRef.current.get(id)?.abort();");
    expect(hookSource).toContain("uploadImage(file, onProgress, controller.signal)");
    expect(hookSource).toContain('uploadResourceFile(file, "file", { fileName: file.name }, onProgress, controller.signal)');
    expect(hookSource).toContain('uploadMediaFile(file, placeholder.type === CanvasNodeType.Video ? "video" : "audio", onProgress, controller.signal)');
});
