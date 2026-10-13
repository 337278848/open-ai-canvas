import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";

import { defaultModelCapabilityConfig } from "../src/lib/model-capabilities";
import { modelQuoteRequest } from "../src/lib/model-pricing";
import { canRetryWorkbenchTask, createWorkbenchSubmissionController, mergeWorkbenchTaskList, restoreWorkbenchReferences, workbenchUploadReference, workbenchVideoRequest } from "../src/pages/workbench/workbench-generation";
import type { GenerationTask } from "../src/services/api/task-center";
import type { WorkbenchTaskReceipt } from "../src/services/api/workbench";
import { defaultConfig, type AiConfig } from "../src/stores/use-config-store";

function configFixture(): AiConfig {
    const capabilityConfig = defaultModelCapabilityConfig();
    const video = capabilityConfig.video!;
    video.operations = ["text_to_video", "image_to_video", "reference_to_video", "audio_to_video"];
    video.references = { ...video.references, minImages: 0, maxImages: 4, maxVideos: 1, maxAudios: 1 };
    video.duration = { selection: "range", min: 1, max: 15, step: 1, default: 6 };
    video.ratios = ["16:9", "9:16"];
    video.defaultRatio = "16:9";
    video.resolutions = ["720p", "1080p"];
    video.defaultResolution = "720p";
    video.generateAudio = { supported: false, default: false };
    video.watermark = { supported: false, default: false };
    const model = "workbench-test::video-declared";
    return {
        ...defaultConfig, channelMode: "remote", model, videoModel: model, models: [model], videoModels: [model],
        channels: [{
            id: "workbench-test", name: "Workbench test", baseUrl: "/api", apiKey: "system", apiFormat: "openai", scope: "system", models: ["video-declared"],
            modelCosts: [{ model: "video-declared", capability: "video", logicalModelId: "video-declared", protocol: "minimax-video", billingMode: "per_second", unitPriceMicrocredits: 1_000_000, capabilityConfig }],
        }],
    };
}

function taskFixture(overrides: Partial<GenerationTask> = {}): GenerationTask {
    return { id: "accepted-task", type: "canvas_video", status: "queued", prompt: "镜头", attempts: 0, createdAt: "2026-10-13T10:00:00Z", updatedAt: "2026-10-13T10:00:00Z", ...overrides };
}

const context = { kind: "video" as const, prompt: "镜头", model: "video-declared", inputJson: JSON.stringify({ workbench: { kind: "video", parameters: { seconds: 8 }, references: [] } }) };

function deferred<T>() {
    let resolve!: (value: T) => void;
    let reject!: (error: unknown) => void;
    const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
    return { promise, resolve, reject };
}

describe("workbench HTTP/IP compatibility", () => {
    test("upload and restore work when secure-context UUID and digest APIs are absent", async () => {
        const descriptor = Object.getOwnPropertyDescriptor(globalThis, "crypto");
        const original = globalThis.crypto;
        Object.defineProperty(globalThis, "crypto", { configurable: true, value: { getRandomValues: original.getRandomValues.bind(original) } });
        const file = new File(["image"], "reference.png", { type: "image/png" });
        let upload: ReturnType<typeof workbenchUploadReference> | undefined;
        try {
            upload = workbenchUploadReference(file, "subject");
            const restored = await restoreWorkbenchReferences([{ resourceId: "resource-1", kind: "image", role: "first_frame", slotKey: "subject" }], async (id) => `https://example.test/${id}`);
            expect(upload).toMatchObject({ kind: "image", name: file.name, slotKey: "subject", uploading: true, bytes: file.size });
            expect(upload.id).toMatch(/^[A-Za-z0-9_-]{32}$/);
            expect(restored[0]).toMatchObject({ resourceId: "resource-1", role: "first_frame", slotKey: "subject", uploading: false });
            expect(restored[0].id).toMatch(/^[A-Za-z0-9_-]{32}$/);
            expect(restored[0].id).not.toBe(upload.id);
        } finally {
            if (upload) URL.revokeObjectURL(upload.previewUrl);
            if (descriptor) Object.defineProperty(globalThis, "crypto", descriptor);
            else delete (globalThis as { crypto?: Crypto }).crypto;
        }
    });
});

describe("workbench video admission", () => {
    for (const [explicit, error] of [
        [{ videoSeconds: "99" }, "时长"],
        [{ size: "1:1" }, "比例"],
        [{ vquality: "2160" }, "分辨率"],
        [{ videoGenerateAudio: "true" }, "声音"],
        [{ videoWatermark: "true" }, "水印"],
    ] as const) {
        test(`explicit ${Object.keys(explicit)[0]} is rejected without changing specifications or references`, () => {
            const config = configFixture();
            const refs = [{ kind: "image" as const, bytes: 4096, resourceId: "resource-1" }];
            const before = structuredClone({ config, refs });
            const request = workbenchVideoRequest(config, config.model, explicit, "镜头", refs);
            expect(request.error).toContain(error);
            expect(request.config).toMatchObject(explicit);
            expect(request.requirements.input?.imageCount).toBe(1);
            expect({ config, refs }).toEqual(before);
        });
    }

    test("unknown capabilities cannot submit", () => {
        const config = configFixture();
        delete config.channels[0].modelCosts![0].capabilityConfig;
        const request = workbenchVideoRequest(config, config.model, { videoSeconds: "8", size: "9:16", vquality: "1080" }, "镜头", []);
        expect(request.assessment.status).toBe("unknown");
        expect(request.error).not.toBe("");
        expect(request.config).toMatchObject({ videoSeconds: "8", size: "9:16", vquality: "1080" });
    });

    test("quote and admission use the same explicit specifications and reference-derived operation", () => {
        const config = configFixture();
        const request = workbenchVideoRequest(config, config.model, { videoSeconds: "8", size: "9:16", vquality: "1080" }, "镜头", [{ kind: "image", bytes: 4096 }]);
        expect(request.error).toBe("");
        expect(request.requirements.videoOperation).toBe("image_to_video");
        expect(request.requirements.media).toEqual([{ kind: "image", bytes: 4096 }]);
        const quote = modelQuoteRequest(request.config, config.model, "video", request.requirements);
        expect(quote?.intent.operation).toBe(request.requirements.videoOperation);
        expect(quote?.intent.options).toMatchObject(request.requirements.options!);
    });

    test("switching to a less capable model keeps and rejects the previous explicit values", () => {
        const config = configFixture();
        const next = structuredClone(config.channels[0].modelCosts![0]);
        next.model = next.logicalModelId = "video-limited";
        next.capabilityConfig!.video!.duration = { selection: "enum", values: [6], default: 6 };
        config.channels[0].models.push(next.model);
        config.channels[0].modelCosts!.push(next);
        config.models.push("workbench-test::video-limited");
        config.videoModels.push("workbench-test::video-limited");
        const explicit = { videoSeconds: "8", size: "9:16", vquality: "1080" };
        expect(workbenchVideoRequest(config, config.model, explicit, "镜头", []).error).toBe("");
        const changed = workbenchVideoRequest(config, "workbench-test::video-limited", explicit, "镜头", []);
        expect(changed.error).toContain("时长");
        expect(changed.config).toMatchObject(explicit);
    });
});

describe("workbench accepted-task boundary", () => {
    test("publishes the accepted receipt before querying and only re-queries the same task after failure", async () => {
        const controller = createWorkbenchSubmissionController();
        const publications: Array<{ task: GenerationTask; accepted: boolean }> = [];
        const queried: string[] = [];
        let creations = 0;
        const queryFailure = new Error("detail endpoint unavailable");
        const result = await controller.submit(async () => { creations++; return { id: "accepted-task", status: "queued" as const, createdAt: undefined }; }, context, async (id) => {
            expect(publications[0]?.accepted).toBe(true);
            expect(publications[0]?.task.id).toBe(id);
            queried.push(id);
            throw queryFailure;
        }, (task, accepted) => publications.push({ task, accepted }));
        expect(result.status).toBe("accepted");
        if (result.status !== "accepted") throw new Error("receipt lost");
        expect(result.detailError).toBe(queryFailure);
        expect(result.task.inputJson).toBe(context.inputJson);
        expect(Number.isFinite(Date.parse(result.task.createdAt))).toBe(true);
        expect(result.task.status).toBe("queued");
        const refreshed = await controller.refresh(result.task, async (id) => { queried.push(id); return taskFixture({ status: "running" }); }, (task, accepted) => publications.push({ task, accepted }));
        expect(creations).toBe(1);
        expect(queried).toEqual(["accepted-task", "accepted-task"]);
        expect(refreshed.detailError).toBeNull();
        expect(publications.map((item) => item.accepted)).toEqual([true, false]);
    });

    test("the existing submit gate blocks concurrent creation and releases at acceptance rather than generation completion", async () => {
        const controller = createWorkbenchSubmissionController();
        const creation = deferred<WorkbenchTaskReceipt["task"]>();
        const detail = deferred<GenerationTask>();
        let calls = 0;
        const create = () => { calls++; return creation.promise; };
        const first = controller.submit(create, context, () => detail.promise, () => {});
        expect(await controller.submit(create, context, () => detail.promise, () => {})).toEqual({ status: "busy" });
        expect(calls).toBe(1);
        creation.resolve({ id: "accepted-task", status: "queued" });
        await Promise.resolve();
        const next = await controller.submit(async () => { calls++; return { id: "next-task", status: "queued" as const }; }, context, async (id) => taskFixture({ id }), () => {});
        expect(next.status).toBe("accepted");
        expect(calls).toBe(2);
        detail.resolve(taskFixture());
        expect((await first).status).toBe("accepted");
    });

    test("a rejected creation releases the gate without publishing a success receipt", async () => {
        const controller = createWorkbenchSubmissionController();
        let published = 0;
        let queried = 0;
        await expect(controller.submit(async () => { throw new Error("creation rejected"); }, context, async () => { queried++; return taskFixture(); }, () => { published++; })).rejects.toThrow("creation rejected");
        expect(published).toBe(0);
        expect(queried).toBe(0);
        expect((await controller.submit(async () => ({ id: "accepted-task", status: "queued" }), context, async () => taskFixture(), () => {})).status).toBe("accepted");
    });

    test("submission_unknown remains non-retryable even with a failed status", () => {
        expect(canRetryWorkbenchTask(taskFixture({ status: "failed", stage: "submission_unknown" }))).toBe(false);
        expect(canRetryWorkbenchTask(taskFixture({ status: "cancelled", stage: "submission_unknown" }))).toBe(false);
        expect(canRetryWorkbenchTask(taskFixture({ status: "running" }))).toBe(false);
        expect(canRetryWorkbenchTask(taskFixture({ status: "failed", stage: "failed" }))).toBe(true);
    });

    test("a stale list cannot erase an accepted task or its restore snapshot", () => {
        const accepted = taskFixture({ inputJson: context.inputJson });
        expect(mergeWorkbenchTaskList([accepted], [])).toEqual([accepted]);
        expect(mergeWorkbenchTaskList([accepted], [taskFixture({ status: "running" })])[0]).toMatchObject({ id: accepted.id, status: "running", inputJson: context.inputJson });
    });
});

test("the shared image/video workbench uses the tested admission, IDs and acceptance controller", () => {
    const page = readFileSync(new URL("../src/pages/workbench/image-workbench-page.tsx", import.meta.url), "utf8");
    expect(page).not.toContain("crypto.randomUUID(");
    expect(page).not.toContain("normalizeVideoValue");
    expect(page).toContain("workbenchUploadReference(file");
    expect(page).toContain("restoreWorkbenchReferences(snapshot.references");
    expect(page).toContain("if (admittedVideo?.error)");
    expect(page).toContain("submission.submit(create");
    expect(page).toContain("submission.refresh(task, queryGenerationTask");
    expect(page).toContain("canRetryWorkbenchTask(latest)");
});
