import { describe, expect, test } from "bun:test";
import { assessModelApplicability } from "../src/lib/model-applicability";
import { defaultModelCapabilityConfig } from "../src/lib/model-capabilities";
import { modelQuoteRequest } from "../src/lib/model-pricing";
import { modelCompatibilityError, type ModelRequirements } from "../src/lib/model-selection";
import { videoCreationAdmission, videoCreationConfig, videoCreationDefaultSelection, videoCreationNodePatch } from "../src/lib/video-creation-admission";
import { applyNodeConfigPatch } from "../src/lib/canvas/canvas-project-domain";
import { buildGenerationConfig } from "../src/lib/canvas/canvas-project-generation";
import { workflowVideoCreationRequirements } from "../src/pages/projects/detail/workflow-video-creation-request";
import { prepareBackendGenerationTask } from "../src/services/api/generation-task";
import { defaultConfig, type AiConfig } from "../src/stores/use-config-store";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";
import type { ReferenceImage } from "../src/types/image";
import type { ReferenceAudio } from "../src/types/media";

function fixture(): AiConfig {
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
    const model = "creation-test::video-declared";
    return {
        ...defaultConfig,
        channelMode: "remote",
        channels: [{
            id: "creation-test",
            name: "Creation test",
            baseUrl: "/api",
            apiKey: "system",
            apiFormat: "openai",
            scope: "system",
            models: ["video-declared"],
            modelCosts: [{
                model: "video-declared",
                displayName: "Declared video",
                capability: "video",
                protocol: "minimax-video",
                logicalModelId: "video-declared",
                billingMode: "per_second",
                unitPriceMicrocredits: 1_000_000,
                capabilityConfig,
            }],
        }],
        models: [model],
        videoModels: [model],
        model,
        videoModel: model,
    };
}

function references(imageCount: number, audioCount = 0) {
    return {
        images: Array.from({ length: imageCount }, (_, index): ReferenceImage => ({
            id: `image-${index}`, name: `image-${index}`, type: "image/png", dataUrl: "", storageKey: `resource:image-${index}`,
        })),
        audios: Array.from({ length: audioCount }, (_, index): ReferenceAudio => ({
            id: `audio-${index}`, name: `audio-${index}`, type: "audio/wav", url: "", storageKey: `resource:audio-${index}`,
        })),
    };
}

function requirements(config: AiConfig): ModelRequirements {
    return workflowVideoCreationRequirements(config, [], [], "镜头缓缓推进");
}

function videoNode(metadata: CanvasNodeData["metadata"]): CanvasNodeData {
    return { id: "video-node", type: CanvasNodeType.Video, title: "Video", position: { x: 0, y: 0 }, width: 400, height: 240, metadata };
}

function personalSameNameConfig(): AiConfig {
    const config = fixture();
    const channel = config.channels[0];
    channel.scope = "user";
    channel.baseUrl = "https://example.test";
    const fixed = channel.modelCosts![0];
    fixed.model = "video-fixed";
    fixed.logicalModelId = undefined;
    fixed.billingMode = "fixed_request";
    fixed.unitPriceMicrocredits = 200_000;
    fixed.pricePolicy = "unified";
    const metered = { ...structuredClone(fixed), model: "video-metered", billingMode: "per_second" as const, unitPriceMicrocredits: 80_000 };
    channel.models = [fixed.model, metered.model];
    channel.modelCosts = [fixed, metered];
    config.videoModels = channel.models.map((model) => `${channel.id}::${model}`);
    config.models = [...config.videoModels];
    config.model = config.videoModels[0];
    config.videoModel = config.model;
    return config;
}

describe("short-drama video filtering, quote and task share one operation", () => {
    for (const [imageCount, audioCount, operation] of [
        [0, 0, "text_to_video"],
        [1, 0, "image_to_video"],
        [2, 0, "image_to_video"],
        [3, 0, "reference_to_video"],
        [0, 1, "audio_to_video"],
        [1, 1, "image_to_video"],
        [2, 1, "image_to_video"],
    ] as const) {
        test(`${imageCount} images and ${audioCount} audio references use ${operation} throughout`, async () => {
            const globalConfig = fixture();
            globalConfig.channels[0].modelCosts![0].logicalCapabilitySpec = {
                version: 1,
                capability: "video",
                operations: ["text_to_video", "image_to_video", "reference_to_video", "audio_to_video"],
                inputs: { image: { min: 0, max: 4 }, audio: { min: 0, max: 1 } },
                options: {
                    size: { values: ["16:9", "9:16"] },
                    videoSeconds: { min: 1, max: 15, step: 1 },
                    vquality: { values: ["720", "1080"] },
                    videoGenerateAudio: { values: [false] },
                    videoWatermark: { values: [false] },
                },
            };
            const config = videoCreationConfig(globalConfig, globalConfig.model);
            const { images, audios } = references(imageCount, audioCount);
            const request = workflowVideoCreationRequirements(config, images, audios, "人物走进画面");
            const admission = videoCreationAdmission(config, request);
            const quote = modelQuoteRequest(config, config.model, "video", request);
            // References already stored as resources do not upload or generate.
            const task = await prepareBackendGenerationTask({
                mode: "video", config, prompt: request.prompt!,
                referenceImages: images, referenceAudios: audios,
                metadata: { videoEditOperation: request.videoOperation },
            });
            expect(admission.error).toBe("");
            expect(modelCompatibilityError(config, config.model, request)).toBe("");
            expect(request.videoOperation).toBe(operation);
            expect(request.videoOperationExplicit).toBe(true);
            expect(admission.requirements.videoOperation).toBe(operation);
            expect(quote?.intent.operation).toBe(operation);
            expect(task.operation).toBe(operation);
            expect(task.input.metadata.videoEditOperation).toBe(operation);
            expect(task.input.referenceImages).toHaveLength(imageCount);
            expect(task.input.referenceAudios).toHaveLength(audioCount);
            expect(quote?.intent.options).toMatchObject(request.options!);
            expect(task.input.capabilityOptions).toMatchObject(request.options!);
        });
    }

    test("two references do not invent first/last-frame support", () => {
        const globalConfig = fixture();
        const config = videoCreationConfig(globalConfig, globalConfig.model);
        const request = workflowVideoCreationRequirements(config, references(2).images, [], "镜头");
        expect(request.videoOperation).toBe("image_to_video");
        expect(videoCreationAdmission(config, request).assessment.summary.join(" ")).toContain("目录未声明首尾帧");
    });

    test("legacy canvas operation is inferred once then marked explicit for every consumer", () => {
        const globalConfig = fixture();
        globalConfig.channels[0].modelCosts![0].capabilityConfig!.video!.operations = ["image_to_video"];
        const config = videoCreationConfig(globalConfig, globalConfig.model);
        const request = workflowVideoCreationRequirements(config, references(1).images, [], "镜头");
        const admission = videoCreationAdmission(config, { ...request, videoOperation: "reference_to_video", videoOperationExplicit: false }, true);
        expect(admission.requirements.videoOperation).toBe("image_to_video");
        expect(admission.requirements.videoOperationExplicit).toBe(true);
        expect(admission.error).toBe("");
        expect(modelQuoteRequest(config, config.model, "video", admission.requirements)?.intent.operation).toBe("image_to_video");
    });

    test("an already-resolved explicit operation is not inferred again", () => {
        const globalConfig = fixture();
        const config = videoCreationConfig(globalConfig, globalConfig.model);
        const request = workflowVideoCreationRequirements(config, references(1).images, [], "镜头");
        const admission = videoCreationAdmission(config, { ...request, videoOperation: "reference_to_video", videoOperationExplicit: true, videoOperationOrigin: "user" });
        expect(admission.requirements.videoOperation).toBe("reference_to_video");
        expect(modelQuoteRequest(config, config.model, "video", admission.requirements)?.intent.operation).toBe("reference_to_video");
    });

    test("only known reference metadata is passed, with milliseconds converted to seconds", () => {
        const config = fixture();
        const { images, audios } = references(2, 1);
        images[0].bytes = 4096;
        audios[0].bytes = 2048;
        audios[0].durationMs = 2500;
        const request = workflowVideoCreationRequirements(config, images, audios);
        expect(request.media).toEqual([
            { kind: "image", bytes: 4096 },
            { kind: "image" },
            { kind: "audio", bytes: 2048, durationSeconds: 2.5 },
        ]);
        expect(images[1].bytes).toBeUndefined();
    });

    test("final prompt and validated duration are rechecked rather than reusing the preview", () => {
        const globalConfig = fixture();
        globalConfig.channels[0].modelCosts![0].capabilityConfig!.video!.references.promptMaxChars = 4;
        const config = videoCreationConfig(globalConfig, globalConfig.model);
        expect(videoCreationAdmission(config, requirements(config)).error).not.toBe("");
        const changed = { ...config, videoSeconds: "16" };
        const request = workflowVideoCreationRequirements(changed, [], [], "镜头");
        expect(videoCreationAdmission(changed, request).error).toContain("时长");
        expect(request.videoSeconds).toBe("16");
    });
});

describe("canvas video intent is preserved until explicit reset", () => {
    test("new nodes use declared defaults instead of incompatible legacy global preferences", () => {
        const config = fixture();
        config.size = "1:1";
        config.videoSeconds = "99";
        config.vquality = "2160";
        config.videoGenerateAudio = "true";
        const request = videoCreationConfig(config, config.model);
        expect(request).toMatchObject({ size: "16:9", videoSeconds: "6", vquality: "720", videoGenerateAudio: "false" });
        expect(videoCreationAdmission(request, requirements(request), true).error).toBe("");
    });

    for (const [explicit, error] of [
        [{ videoSeconds: "99" }, "时长"],
        [{ vquality: "2160" }, "分辨率"],
        [{ size: "1:1" }, "比例"],
        [{ videoGenerateAudio: "true" }, "声音"],
        [{ videoWatermark: "true" }, "水印"],
    ] as const) {
        test(`explicit ${Object.keys(explicit)[0]} is not silently downgraded`, () => {
            const config = fixture();
            const request = videoCreationConfig(config, config.model, explicit);
            expect(request).toMatchObject(explicit);
            expect(videoCreationAdmission(request, requirements(request), true).error).toContain(error);
        });
    }

    test("explicit specs survive the real canvas model-switch reset and executor config", () => {
        const globalConfig = fixture();
        const config = videoCreationConfig(globalConfig, globalConfig.model, { size: "9:16", videoSeconds: "8", vquality: "1080p" });
        const node = videoNode({ model: "old-model", seconds: "6", size: "16:9", vquality: "720" });
        const next = applyNodeConfigPatch(node, videoCreationNodePatch(config));
        expect(next.metadata).toMatchObject({ model: config.model, seconds: "8", size: "9:16", vquality: "1080p" });
        expect(buildGenerationConfig(globalConfig, next, "video", requirements(config))).toMatchObject({
            model: config.model, videoSeconds: "8", size: "9:16", vquality: "1080",
        });
        expect(node.metadata?.model).toBe("old-model");
    });

    test("unknown or incomplete catalogue entries cannot submit", () => {
        const config = fixture();
        delete config.channels[0].modelCosts![0].capabilityConfig;
        const request = videoCreationConfig(config, config.model);
        expect(videoCreationAdmission(request, requirements(request), true)).toMatchObject({
            assessment: { status: "unknown" },
        });
        expect(videoCreationAdmission(request, requirements(request), true).error).toContain("未声明");
        config.channels[0].modelCosts![0].capabilityConfig = { version: 1, video: {} as never };
        const incompleteRequest = videoCreationConfig(config, config.model);
        expect(videoCreationAdmission(incompleteRequest, requirements(incompleteRequest), true).error).not.toBe("");
    });

    test("a more expensive compatible route requires explicit confirmation without changing the request", () => {
        const globalConfig = fixture();
        const channel = globalConfig.channels[0];
        const original = channel.modelCosts![0];
        const alternate = structuredClone(original);
        original.capabilityConfig!.video!.operations = ["text_to_video"];
        original.capabilityConfig!.video!.references.maxImages = 0;
        alternate.model = "video-image";
        alternate.logicalModelId = alternate.model;
        alternate.unitPriceMicrocredits = 9_000_000;
        channel.modelCosts!.push(alternate);
        channel.models.push(alternate.model);
        globalConfig.videoModels.push(`creation-test::${alternate.model}`);
        globalConfig.models = [...globalConfig.videoModels];
        const config = videoCreationConfig(globalConfig, globalConfig.model);
        const req = workflowVideoCreationRequirements(config, references(1).images, [], "镜头");
        const admission = videoCreationAdmission(config, req, true);
        expect(admission.error).not.toBe("");
        expect(admission.suggestedModel).toBe("creation-test::video-image");
        expect(config.model).toBe(globalConfig.model);
        expect(buildGenerationConfig(globalConfig, videoNode(videoCreationNodePatch(config)), "video", req).model).toBe(admission.suggestedModel);
        const confirmed = { ...config, model: admission.suggestedModel };
        expect(videoCreationAdmission(confirmed, req, true).error).toBe("");
        expect(modelQuoteRequest(confirmed, confirmed.model, "video", req)?.logicalModelID).toBe("video-image");
    });

    test("explicit reset unlocks disjoint model specs but never removes media constraints", () => {
        const config = fixture();
        const requestConfig = videoCreationConfig(config, config.model, { size: "16:9", videoSeconds: "6", vquality: "720" });
        const req = workflowVideoCreationRequirements(requestConfig, references(1).images, [], "镜头");
        const profile = config.channels[0].modelCosts![0].capabilityConfig!.video!;
        profile.ratios = ["9:16"];
        profile.defaultRatio = "9:16";
        profile.resolutions = ["1080p"];
        profile.defaultResolution = "1080p";
        profile.duration = { selection: "enum", values: [10], default: 10 };
        expect(assessModelApplicability(config, config.model, req).status).toBe("incompatible");
        const selection = videoCreationDefaultSelection(req);
        expect(assessModelApplicability(config, config.model, selection).status).toBe("ready");
        expect(selection.input).toEqual(req.input);
        expect(selection.media).toEqual(req.media);
        expect(selection.prompt).toBe("镜头");
        expect(req.options?.vquality).toBe("720");
        expect(videoCreationConfig(config, config.model)).toMatchObject({ size: "9:16", videoSeconds: "10", vquality: "1080" });
        profile.references.maxImages = 0;
        expect(assessModelApplicability(config, config.model, selection).status).toBe("incompatible");
    });
});

describe("compatible personal video choices are not rerouted by unit price", () => {
    test("admission keeps the chosen 0.2/request model instead of requiring a 0.8 total alternative", () => {
        const config = personalSameNameConfig();
        const requested = videoCreationConfig(config, config.model, { videoSeconds: "10" });
        const req = requirements(requested);
        const selected = assessModelApplicability(requested, config.model, req);
        const alternative = assessModelApplicability(requested, config.videoModels[1], req);
        expect(selected).toMatchObject({ status: "ready", estimatedCredits: 0.2 });
        expect(alternative).toMatchObject({ status: "ready", estimatedCredits: 0.8 });
        const admission = videoCreationAdmission(requested, req, true);
        expect(admission.error).toBe("");
        expect(admission.suggestedModel).toBe("");
        expect(admission.assessment.estimatedCredits).toBe(0.2);
    });

    for (const type of [CanvasNodeType.Video, CanvasNodeType.Config]) {
        test(`actual ${type} executor retains the chosen model on initial, input and hydrated passes`, async () => {
            const config = personalSameNameConfig();
            // The node's concrete selection wins even when the global default differs.
            config.videoModel = config.videoModels[1];
            for (const chosen of config.videoModels) {
                const requested = videoCreationConfig(config, chosen, { videoSeconds: "10" });
                const node = { ...videoNode(videoCreationNodePatch(requested)), type };
                const req = requirements(requested);
                for (const currentRequirements of [undefined, req, { ...req, media: [{ kind: "image" as const, bytes: 1024 }], input: { ...req.input!, imageCount: 1 }, videoOperation: "image_to_video" }]) {
                    const generation = buildGenerationConfig(config, node, "video", currentRequirements);
                    expect(generation.model).toBe(chosen);
                    expect(generation.videoSeconds).toBe("10");
                    const task = await prepareBackendGenerationTask({
                        mode: "video", config: generation, prompt: "镜头",
                        referenceImages: references(currentRequirements?.input?.imageCount || 0).images,
                        metadata: { videoEditOperation: currentRequirements?.videoOperation },
                    });
                    expect(task.model).toBe(chosen);
                    expect(task.input.config.model).toBe(chosen.split("::")[1]);
                }
            }
        });
    }

    test("legacy image grouping still chooses the lowest-priced compatible variant", () => {
        const config = personalSameNameConfig();
        for (const cost of config.channels[0].modelCosts!) {
            cost.capability = "image";
            cost.protocol = "gemini-image";
            cost.billingMode = "fixed_request";
        }
        config.imageModels = [...config.models];
        config.imageModel = config.models[0];
        const node = { ...videoNode({ model: config.imageModel, size: "1:1" }), type: CanvasNodeType.Image };
        const generation = buildGenerationConfig(config, node, "image", { capability: "image", imageSize: "1:1" });
        expect(generation.model).toBe(config.models[1]);
    });
});

describe("video discovery distinguishes user intent from inferred execution modes", () => {
    function imageOnlyConfig() {
        const config = fixture();
        const video = config.channels[0].modelCosts![0].capabilityConfig!.video!;
        video.operations = ["image_to_video"];
        video.references.minImages = 1;
        video.references.maxImages = 1;
        return videoCreationConfig(config, config.model);
    }

    test("canvas reset releases inferred text-to-video but still blocks generation until the image exists", () => {
        const config = imageOnlyConfig();
        const admission = videoCreationAdmission(config, {
            capability: "video",
            input: { textCount: 1, imageCount: 0, videoCount: 0, audioCount: 0, characterCount: 0 },
            prompt: "镜头",
        }, true);
        expect(admission.requirements.videoOperation).toBe("text_to_video");
        expect(admission.requirements.videoOperationExplicit).toBe(true);
        expect(admission.requirements.videoOperationOrigin).toBe("inferred");
        expect(admission.selectionRequirements.videoOperation).toBeUndefined();
        expect(admission.selectionRequirements.videoSeconds).toBe(config.videoSeconds);
        expect(admission.selectionRequirements.options).toEqual(admission.requirements.options);
        const selection = videoCreationDefaultSelection(admission.requirements);
        expect(selection.videoOperation).toBeUndefined();
        expect(assessModelApplicability(config, config.model, selection).status).toBe("needs_input");
        expect(admission.assessment.status).toBe("needs_input");
        expect(admission.error).not.toBe("");
        expect(admission.requirements.input?.imageCount).toBe(0);
    });

    test("short-drama reset keeps inferred operation out of discovery without changing the submitted mode", async () => {
        const config = imageOnlyConfig();
        const request = workflowVideoCreationRequirements(config, [], [], "镜头");
        const before = structuredClone(request);
        const selection = videoCreationDefaultSelection(request);
        expect(request.videoOperationOrigin).toBe("inferred");
        expect(selection.videoOperation).toBeUndefined();
        expect(assessModelApplicability(config, config.model, selection).status).toBe("needs_input");
        expect(request).toEqual(before);
        const images = references(1).images;
        const filled = workflowVideoCreationRequirements(config, images, [], "镜头");
        expect(videoCreationAdmission(config, filled).error).toBe("");
        const task = await prepareBackendGenerationTask({
            mode: "video", config, prompt: filled.prompt!, referenceImages: images,
            metadata: { videoEditOperation: filled.videoOperation },
        });
        expect(filled.videoOperation).toBe("image_to_video");
        expect(filled.videoOperationExplicit).toBe(true);
        expect(task.operation).toBe(filled.videoOperation);
        expect(modelQuoteRequest(config, config.model, "video", filled)?.intent.operation).toBe(task.operation);
    });

    test("rechecking a resolved automatic request does not turn it into user intent", () => {
        const config = imageOnlyConfig();
        const request = workflowVideoCreationRequirements(config, [], [], "镜头");
        const first = videoCreationAdmission(config, request);
        const repeated = videoCreationAdmission(config, first.requirements);
        expect(first.assessment.status).toBe("needs_input");
        expect(repeated.assessment.status).toBe("needs_input");
        expect(videoCreationDefaultSelection(repeated.requirements).videoOperation).toBeUndefined();
        expect(repeated.requirements).toEqual(first.requirements);
    });

    test("a genuinely chosen text-to-video operation survives reset and cannot become an image suggestion", () => {
        const config = imageOnlyConfig();
        const admission = videoCreationAdmission(config, {
            capability: "video",
            input: { textCount: 1, imageCount: 0, videoCount: 0, audioCount: 0, characterCount: 0 },
            videoOperation: "text_to_video",
            videoOperationExplicit: true,
        });
        const selection = videoCreationDefaultSelection(admission.requirements);
        expect(selection.videoOperation).toBe("text_to_video");
        expect(selection.videoOperationExplicit).toBe(true);
        expect(assessModelApplicability(config, config.model, selection).status).toBe("incompatible");
    });

    test("legacy inpaint intent is retained for discovery and reset", () => {
        const config = imageOnlyConfig();
        const admission = videoCreationAdmission(config, {
            capability: "video",
            input: { textCount: 1, imageCount: 1, videoCount: 0, audioCount: 0, characterCount: 0 },
            videoOperation: "inpaint",
        });
        const selection = videoCreationDefaultSelection(admission.requirements);
        expect(selection.videoOperation).toBe("inpaint");
        expect(selection.videoOperationExplicit).toBe(true);
        expect(assessModelApplicability(config, config.model, selection).status).toBe("incompatible");
        expect(modelQuoteRequest(config, config.model, "video", admission.requirements)?.intent.operation).toBe("inpaint");
    });
});
