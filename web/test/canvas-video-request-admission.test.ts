import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { buildCanvasVideoRequestConfig, canvasVideoMediaMetadata } from "../src/lib/canvas/canvas-video-admission";
import { buildGenerationConfig } from "../src/lib/canvas/canvas-project-generation";
import { defaultModelCapabilityConfig } from "../src/lib/model-capabilities";
import { videoCreationAdmission } from "../src/lib/video-creation-admission";
import { prepareBackendGenerationTask } from "../src/services/api/generation-task";
import { defaultConfig, type AiConfig } from "../src/stores/use-config-store";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";

function fixture() {
    const profile = defaultModelCapabilityConfig();
    profile.video!.duration = { selection: "range", min: 1, max: 15, step: 1, default: 6 };
    profile.video!.ratios = ["16:9", "9:16"];
    profile.video!.defaultRatio = "16:9";
    profile.video!.resolutions = ["720p", "1080p"];
    profile.video!.defaultResolution = "720p";
    const model = "gate::video";
    const config: AiConfig = {
        ...defaultConfig, model, videoModel: model, models: [model], videoModels: [model],
        channels: [{
            id: "gate", name: "Test", scope: "system", enabled: true, apiFormat: "openai", apiKey: "system", baseUrl: "/api/test",
            models: ["video"], modelCosts: [{ model: "video", capability: "video", billingMode: "fixed_request", unitPriceMicrocredits: 200_000, capabilityConfig: profile }],
        }],
    };
    const node: CanvasNodeData = {
        id: "result", type: CanvasNodeType.Video, title: "视频", position: { x: 0, y: 0 }, width: 320, height: 180,
        metadata: { model, prompt: "镜头", size: "16:9", seconds: "6", vquality: "720", generateAudio: "false", watermark: "false" },
    };
    const input = { textCount: 1, imageCount: 0, videoCount: 0, audioCount: 0, characterCount: 0 };
    return { config, node, input };
}

describe("all canvas video entrypoints validate the original request", () => {
    test("retry cannot turn saved 99 seconds into a valid 15-second paid request", () => {
        const { config, node, input } = fixture();
        node.metadata!.seconds = "99";
        const legacy = buildGenerationConfig(config, node, "video");
        const request = buildCanvasVideoRequestConfig(config, node, legacy);
        expect(request.videoSeconds).toBe("99");
        expect(videoCreationAdmission(request, { input, prompt: "镜头" }).error).toContain("时长");
        expect(node.metadata!.seconds).toBe("99");
    });

    test("missing capabilities stay unknown even after legacy config construction", () => {
        const { config, node, input } = fixture();
        delete config.channels[0].modelCosts![0].capabilityConfig;
        const request = buildCanvasVideoRequestConfig(config, node, buildGenerationConfig(config, node, "video"));
        expect(videoCreationAdmission(request, { input })).toMatchObject({ assessment: { status: "unknown" } });
        expect(videoCreationAdmission(request, { input }).error).not.toBe("");
    });

    test("no fallback can mask an unavailable saved model", () => {
        const { config, node } = fixture();
        node.metadata!.model = "deleted::video";
        expect(() => buildGenerationConfig(config, node, "video")).toThrow("不可用");
    });

    test("valid saved specs and selected identity survive actual task serialization", async () => {
        const { config, node, input } = fixture();
        Object.assign(node.metadata!, { seconds: "8", size: "9:16", vquality: "1080" });
        const request = buildCanvasVideoRequestConfig(config, node, buildGenerationConfig(config, node, "video"));
        expect(videoCreationAdmission(request, { input, prompt: "镜头" }).error).toBe("");
        const task = await prepareBackendGenerationTask({ mode: "video", prompt: "镜头", config: request });
        expect((task.input as { config: object }).config).toMatchObject({ model: "video", videoSeconds: "8", size: "9:16", vquality: "1080", videoGenerateAudio: "false" });
    });

    test("known reference metadata and final expanded prompt remain in admission", () => {
        const { config, node, input } = fixture();
        const profile = config.channels[0].modelCosts![0].capabilityConfig!.video!;
        profile.references.promptMaxChars = 4;
        const request = buildCanvasVideoRequestConfig(config, node);
        expect(videoCreationAdmission(request, { input, prompt: "技能扩展后的完整提示词" }).error).toContain("字符");
        expect(canvasVideoMediaMetadata({
            referenceImages: [{ bytes: 500 }],
            referenceVideos: [{ durationMs: 2500 }],
            referenceAudios: [{}],
        })).toEqual([
            { kind: "image", bytes: 500 },
            { kind: "video", bytes: undefined, durationSeconds: 2.5 },
            { kind: "audio", bytes: undefined, durationSeconds: undefined },
        ]);
    });

    test("retry and batch-backed executor wire admission before starting a request", () => {
        const retry = readFileSync(new URL("../src/pages/canvas/use-canvas-generation-retry.ts", import.meta.url), "utf8");
        const executor = readFileSync(new URL("../src/pages/canvas/use-canvas-generation-executor.ts", import.meta.url), "utf8");
        expect(retry).toContain("buildCanvasVideoRequestConfig(effectiveConfig, videoRequestNode, legacyConfig)");
        expect(retry.indexOf("if (admission.error)")).toBeLessThan(retry.indexOf("setRunningNodeId(node.id)"));
        expect(executor).toContain("if (!validateVideoRequest) generationConfig = buildGenerationConfig");
        expect(executor).toContain("videoCreationAdmission(generationConfig, hydratedRequirements).error");
        expect(executor).toContain("options?.waitForTaskCapacity) setNodes");
    });
});
