import { describe, expect, test } from "bun:test";
import type { CanvasVideoSegmentItem } from "../src/components/canvas/canvas-video-segment-dialog";
import { buildCanvasVideoSegmentRequest } from "../src/lib/canvas/canvas-video-regeneration";
import { buildGenerationConfig } from "../src/lib/canvas/canvas-project-generation";
import { assessModelApplicability } from "../src/lib/model-applicability";
import { defaultModelCapabilityConfig } from "../src/lib/model-capabilities";
import { videoCreationNodePatch } from "../src/lib/video-creation-admission";
import { prepareBackendGenerationTask } from "../src/services/api/generation-task";
import { defaultConfig, type AiConfig } from "../src/stores/use-config-store";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";

const eightSecondModel = "segment-test::eight-seconds";
const sixSecondModel = "segment-test::six-seconds";
const segments: CanvasVideoSegmentItem[] = [
    { id: "first", startMs: 2000, endMs: 5000, sourceNodeId: "source" },
    { id: "second", startMs: 5000, endMs: 9000, sourceNodeId: "source" },
];

function fixture(): AiConfig {
    const capabilityConfig = defaultModelCapabilityConfig("newapi-channel-2", "eight-seconds");
    capabilityConfig.video = {
        ...capabilityConfig.video!,
        references: { ...capabilityConfig.video!.references, minImages: 0, maxImages: 0, maxVideos: 1, maxAudios: 0, maxVideoDurationSeconds: 5 },
        duration: { selection: "enum", values: [8], default: 8 },
        ratios: ["9:16"], defaultRatio: "9:16",
        resolutions: ["768p"], defaultResolution: "768p",
        generateAudio: { supported: false, default: false },
        watermark: { supported: false, default: false },
        operations: ["extend", "reference_to_video", "inpaint"],
        defaultOperation: "extend",
    };
    const other = structuredClone(capabilityConfig);
    other.video!.duration = { selection: "enum", values: [6], default: 6 };
    other.video!.resolutions = ["720p"];
    other.video!.defaultResolution = "720p";
    return {
        ...structuredClone(defaultConfig),
        model: sixSecondModel, videoModel: sixSecondModel,
        videoSeconds: "6", size: "1:1", vquality: "2160", videoGenerateAudio: "true", videoWatermark: "true",
        models: [eightSecondModel, sixSecondModel], videoModels: [eightSecondModel, sixSecondModel],
        channels: [{
            id: "segment-test", name: "Segment test", scope: "user",
            apiKey: "test-key", baseUrl: "https://example.test", apiFormat: "openai", interfaceType: "newapi-channel-2",
            models: ["eight-seconds", "six-seconds"],
            modelCosts: [
                { model: "eight-seconds", displayName: "Eight", capability: "video", protocol: "newapi-channel-2", billingMode: "per_second", unitPriceMicrocredits: 2, capabilityConfig },
                { model: "six-seconds", displayName: "Six", capability: "video", protocol: "newapi-channel-2", billingMode: "per_second", unitPriceMicrocredits: 1, capabilityConfig: other },
            ],
        }],
    };
}

function targetNode(request: ReturnType<typeof buildCanvasVideoSegmentRequest>): CanvasNodeData {
    return {
        id: "target", type: CanvasNodeType.Video, title: "待生成片段", position: { x: 0, y: 0 }, width: 400, height: 240,
        metadata: { ...videoCreationNodePatch(request.config), prompt: request.prompt, videoEditOperation: request.operation, generationMode: "video", status: "idle" },
    };
}

describe("video segment model applicability and request handoff", () => {
    test("a global six-second preference does not exclude an eight-second candidate", () => {
        const config = fixture();
        const snapshot = structuredClone(config);
        const request = buildCanvasVideoSegmentRequest(config, sixSecondModel, segments, "续写镜头");
        expect(request.requirements.videoSeconds).toBe("6");
        expect(request.selectionRequirements.videoSeconds).toBeUndefined();
        expect(request.selectionRequirements.options).toBeUndefined();
        expect(request.selectionRequirements.videoOperation).toBeUndefined();
        expect(assessModelApplicability(config, eightSecondModel, request.selectionRequirements).status).toBe("ready");
        const selected = buildCanvasVideoSegmentRequest(config, eightSecondModel, segments, "续写镜头");
        expect(selected.error).toBe("");
        expect(selected.config).toMatchObject({ model: eightSecondModel, videoModel: eightSecondModel, videoSeconds: "8", size: "9:16", vquality: "768", videoGenerateAudio: "false", videoWatermark: "false" });
        expect(config).toEqual(snapshot);
    });

    test("separate segments are one reference per task, with the longest segment checked", () => {
        const config = fixture();
        const request = buildCanvasVideoSegmentRequest(config, eightSecondModel, segments, "");
        expect(request.requirements.input?.videoCount).toBe(1);
        expect(request.requirements.media).toEqual([{ kind: "video", durationSeconds: 4 }]);
        expect(request.prompt).toBe("保持画面主体与镜头，重新生成这一段视频");
        expect(request.error).toBe("");
        const tooLong = [...segments, { id: "long", startMs: 0, endMs: 6000 }];
        const snapshot = structuredClone(tooLong);
        expect(buildCanvasVideoSegmentRequest(config, eightSecondModel, tooLong, "").error).toContain("时长");
        expect(tooLong).toEqual(snapshot);
    });

    test("known-invalid ranges, missing declarations and incompatible explicit operations cannot confirm", () => {
        const config = fixture();
        for (const endMs of [0, 99, NaN, Infinity]) {
            expect(buildCanvasVideoSegmentRequest(config, eightSecondModel, [{ id: "invalid", startMs: 0, endMs }], "").error).not.toBe("");
        }
        expect(buildCanvasVideoSegmentRequest(config, eightSecondModel, [], "").error).toContain("至少添加");
        expect(buildCanvasVideoSegmentRequest(config, eightSecondModel, segments, "", "camera_motion").error).not.toBe("");
        delete config.channels[0].modelCosts![0].capabilityConfig;
        expect(buildCanvasVideoSegmentRequest(config, eightSecondModel, segments, "")).toMatchObject({ assessment: { status: "unknown" } });
        expect(buildCanvasVideoSegmentRequest(config, eightSecondModel, segments, "").error).not.toBe("");
    });

    test("confirmation carries the displayed defaults instead of rereading a changed default", () => {
        const config = fixture();
        const preview = buildCanvasVideoSegmentRequest(config, eightSecondModel, segments, "保留片段");
        const profile = config.channels[0].modelCosts![0].capabilityConfig!.video!;
        profile.duration = { selection: "enum", values: [4, 8], default: 4 };
        const confirmed = buildCanvasVideoSegmentRequest(config, preview.config.model, segments, preview.prompt, preview.operation, preview.generationSettings);
        expect(confirmed.error).toBe("");
        expect(confirmed.generationSettings).toEqual(preview.generationSettings);
        expect(targetNode(confirmed).metadata).toMatchObject({ model: eightSecondModel, seconds: "8", size: "9:16", vquality: "768", generateAudio: "false", watermark: "false" });
        profile.duration.values = [4];
        expect(buildCanvasVideoSegmentRequest(config, preview.config.model, segments, preview.prompt, preview.operation, preview.generationSettings).error).toContain("时长");
    });

    test("the actual prepared payload matches the model, specs and operation shown in the dialog", async () => {
        const config = fixture();
        const request = buildCanvasVideoSegmentRequest(config, eightSecondModel, segments, "保留镜头", "inpaint");
        expect(request.error).toBe("");
        expect(request.selectionRequirements.videoOperation).toBe("inpaint");
        const generationConfig = buildGenerationConfig(config, targetNode(request), "video", request.requirements);
        const task = await prepareBackendGenerationTask({
            mode: "video", config: generationConfig, prompt: request.prompt,
            referenceVideos: [{ id: "clip", name: "clip.mp4", type: "video/mp4", url: "", storageKey: "resource:segment-clip", durationMs: 3000 }],
            metadata: { videoEditOperation: request.operation },
        });
        expect(task.model).toBe(eightSecondModel);
        expect(task.operation).toBe(request.operation);
        expect(task.input.metadata.videoEditOperation).toBe(request.operation);
        expect(task.input.referenceVideos).toHaveLength(1);
        expect(task.input.config).toMatchObject(request.generationSettings);
    });

    test("an automatic mode uses the same operation as the real backend task constructor", async () => {
        const config = fixture();
        const request = buildCanvasVideoSegmentRequest(config, eightSecondModel, segments, "");
        const task = await prepareBackendGenerationTask({
            mode: "video", config: request.config, prompt: request.prompt,
            referenceVideos: [{ id: "clip", name: "clip.mp4", type: "video/mp4", url: "", storageKey: "resource:segment-clip", durationMs: 3000 }],
            metadata: { videoEditOperation: request.operation },
        });
        expect(request.error).toBe("");
        expect(task.operation).toBe(request.operation);
    });

    test("a concrete expensive model is not replaced by its cheaper display-name sibling", async () => {
        const config = fixture();
        const cost = config.channels[0].modelCosts![0];
        const cheap = structuredClone(cost);
        cheap.model = "cheap-eight";
        cheap.unitPriceMicrocredits = 1;
        config.channels[0].models.push(cheap.model);
        config.channels[0].modelCosts!.push(cheap);
        config.models.push(`segment-test::${cheap.model}`);
        config.videoModels.push(`segment-test::${cheap.model}`);
        const request = buildCanvasVideoSegmentRequest(config, eightSecondModel, segments, "镜头");
        expect(request.error).toBe("");
        expect(request.config.model).toBe(eightSecondModel);
        // Exercise the real persisted-node-to-task path, not a copy of the request builder.
        const generationConfig = buildGenerationConfig(config, targetNode(request), "video", request.requirements);
        const task = await prepareBackendGenerationTask({
            mode: "video", config: generationConfig, prompt: request.prompt,
            referenceVideos: [{ id: "clip", name: "clip.mp4", type: "video/mp4", url: "", storageKey: "resource:segment-clip", durationMs: 3000 }],
            metadata: { videoEditOperation: request.operation },
        });
        expect(task.model).toBe(eightSecondModel);
        expect(task.input.config).toMatchObject(request.generationSettings);
    });
});
