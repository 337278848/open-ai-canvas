import { describe, expect, test } from "bun:test";
import { assessModelApplicability } from "../src/lib/model-applicability";
import { defaultModelCapabilityConfig } from "../src/lib/model-capabilities";
import { modelQuoteRequest } from "../src/lib/model-pricing";
import { modelRequestOptions, resolveModelGenerationDefaults, resolveModelVideoBooleanOptions, type ModelRequirements } from "../src/lib/model-selection";
import { videoCreationConfig } from "../src/lib/video-creation-admission";
import { prepareBackendGenerationTask } from "../src/services/api/generation-task";
import { defaultConfig, type AiConfig } from "../src/stores/use-config-store";

const model = "boolean-contract::logical-video";
const enabled = { videoGenerateAudio: "true", videoWatermark: "true" };
const disabled = { videoGenerateAudio: "false", videoWatermark: "false" };

function fixture(): AiConfig {
    return {
        ...structuredClone(defaultConfig),
        model,
        videoModel: model,
        models: [model],
        videoModels: [model],
        channels: [{
            id: "boolean-contract",
            name: "Boolean contract",
            scope: "system",
            apiFormat: "openai",
            baseUrl: "/api/boolean-contract",
            apiKey: "system",
            models: ["logical-video"],
            modelCosts: [{
                model: "logical-video",
                capability: "video",
                protocol: "newapi",
                logicalModelId: "logical-video",
                billingMode: "fixed_request",
                unitPriceMicrocredits: 1_000_000,
                logicalCapabilitySpec: {
                    version: 1,
                    capability: "video",
                    operations: ["text_to_video"],
                    inputs: {},
                    options: {
                        size: { values: ["16:9"] },
                        videoSeconds: { values: [6] },
                        vquality: { values: ["720p"] },
                        videoGenerateAudio: { values: [false, true] },
                        videoWatermark: { values: [false, true] },
                    },
                },
                defaultOptions: { size: "16:9", videoSeconds: 6, vquality: "720p", videoGenerateAudio: true, videoWatermark: true },
            }],
        }],
    };
}

function cost(config: AiConfig) {
    return config.channels[0].modelCosts![0];
}

function requirements(config: AiConfig): ModelRequirements {
    return {
        capability: "video",
        input: { textCount: 1, imageCount: 0, videoCount: 0, audioCount: 0, characterCount: 0 },
        videoSeconds: config.videoSeconds,
        options: modelRequestOptions(config, "video"),
    };
}

describe("declared logical video boolean contract", () => {
    test("logical defaults replace legacy preferences without requiring a synthetic video profile", () => {
        const config = fixture();
        const before = structuredClone(config);
        expect(cost(config).capabilityConfig).toBeUndefined();
        expect(resolveModelVideoBooleanOptions(config, model, {}, disabled)).toEqual(enabled);
        expect(resolveModelGenerationDefaults(config, model, "video", {}, disabled)).toMatchObject(enabled);
        expect(config).toEqual(before);
    });

    for (const audio of [false, true]) {
        for (const watermark of [false, true]) {
            test(`real task payload and quote preserve audio=${audio}, watermark=${watermark}`, async () => {
                const config = videoCreationConfig(fixture(), model, {
                    videoGenerateAudio: String(audio),
                    videoWatermark: String(watermark),
                });
                const request = requirements(config);
                expect(assessModelApplicability(config, model, request).status).toBe("ready");
                const quote = modelQuoteRequest(config, model, "video", request);
                // No task is created and no media is uploaded: exercise the real serializer only.
                const task = await prepareBackendGenerationTask({ mode: "video", prompt: "镜头缓缓推进", config });
                expect(task.logicalModelId).toBe("logical-video");
                expect(task.input.config).toMatchObject({ videoGenerateAudio: String(audio), videoWatermark: String(watermark) });
                expect(task.input.capabilityOptions).toMatchObject({ videoGenerateAudio: audio, videoWatermark: watermark });
                expect(quote?.intent.options).toMatchObject(task.input.capabilityOptions!);
            });
        }
    }

    test("string boolean declarations and defaults use the same contract", async () => {
        const config = fixture();
        for (const key of ["videoGenerateAudio", "videoWatermark"] as const) {
            cost(config).logicalCapabilitySpec!.options![key] = { values: ["false", "true"] };
            cost(config).defaultOptions![key] = "true";
        }
        expect(resolveModelVideoBooleanOptions(config, model)).toEqual(enabled);
        const requestConfig = videoCreationConfig(config, model);
        expect(assessModelApplicability(requestConfig, model, requirements(requestConfig)).status).toBe("ready");
        const task = await prepareBackendGenerationTask({ mode: "video", prompt: "镜头", config: requestConfig });
        expect(task.input.config).toMatchObject(enabled);
        expect(task.input.capabilityOptions).toMatchObject({ videoGenerateAudio: true, videoWatermark: true });
    });

    test("profiles-only contracts keep supported switches through task serialization", async () => {
        const config = fixture();
        cost(config).logicalCapabilityProfiles = [cost(config).logicalCapabilitySpec!];
        delete cost(config).logicalCapabilitySpec;
        const requestConfig = videoCreationConfig(config, model);
        expect(resolveModelVideoBooleanOptions(config, model, enabled)).toEqual(enabled);
        expect(assessModelApplicability(requestConfig, model, requirements(requestConfig)).status).toBe("ready");
        const task = await prepareBackendGenerationTask({ mode: "video", prompt: "镜头", config: requestConfig });
        expect(task.input.config).toMatchObject(enabled);
    });

    for (const key of ["videoGenerateAudio", "videoWatermark"] as const) {
        test(`explicit unsupported ${key} stays blocked in the UI and off in both payloads`, async () => {
            const config = fixture();
            cost(config).logicalCapabilitySpec!.options![key] = { values: [false] };
            cost(config).defaultOptions![key] = false;
            const requestConfig = videoCreationConfig(config, model, { [key]: "true" });
            expect(assessModelApplicability(requestConfig, model, requirements(requestConfig)).status).toBe("incompatible");
            expect(resolveModelVideoBooleanOptions(config, model, enabled)[key]).toBe("false");
            const task = await prepareBackendGenerationTask({ mode: "video", prompt: "镜头", config: requestConfig });
            expect(task.input.config[key]).toBe("false");
            expect(task.input.capabilityOptions?.[key]).toBe(false);
        });
    }

    test("defaultOptions alone and protocol fallbacks do not authorize an undeclared switch", () => {
        const config = fixture();
        cost(config).protocol = "volcengine-ark-video";
        delete cost(config).logicalCapabilitySpec;
        expect(resolveModelVideoBooleanOptions(config, model, enabled, enabled)).toEqual(disabled);
        expect(resolveModelGenerationDefaults(config, model, "video", enabled, enabled)).toMatchObject(disabled);
        expect(assessModelApplicability(config, model, requirements(config)).status).toBe("unknown");
    });

    test("a logical spec that omits boolean options does not gain them from defaults or protocol", () => {
        const config = fixture();
        cost(config).protocol = "volcengine-ark-video";
        delete cost(config).logicalCapabilitySpec!.options!.videoGenerateAudio;
        delete cost(config).logicalCapabilitySpec!.options!.videoWatermark;
        expect(resolveModelVideoBooleanOptions(config, model, enabled, enabled)).toEqual(disabled);
    });

    test("existing concrete profiles retain their hard unsupported guard", async () => {
        const config = fixture();
        cost(config).capabilityConfig = defaultModelCapabilityConfig();
        const requestConfig = { ...videoCreationConfig(config, model), ...enabled };
        expect(assessModelApplicability(requestConfig, model, requirements(requestConfig)).status).toBe("incompatible");
        expect(resolveModelVideoBooleanOptions(config, model, enabled)).toEqual(disabled);
        const task = await prepareBackendGenerationTask({ mode: "video", prompt: "镜头", config: requestConfig });
        expect(task.input.config).toMatchObject(disabled);
        expect(task.input.capabilityOptions).toMatchObject({ videoGenerateAudio: false, videoWatermark: false });
    });

    test("explicit false survives enabled concrete profile defaults", () => {
        const config = fixture();
        const profile = defaultModelCapabilityConfig();
        profile.video!.generateAudio = { supported: true, default: true };
        profile.video!.watermark = { supported: true, default: true };
        cost(config).capabilityConfig = profile;
        expect(resolveModelVideoBooleanOptions(config, model, {}, disabled)).toEqual(enabled);
        expect(resolveModelVideoBooleanOptions(config, model, disabled, enabled)).toEqual(disabled);
    });
});
