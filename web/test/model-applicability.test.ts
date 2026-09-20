import { describe, expect, test } from "bun:test";
import { assessModelApplicability, buildModelApplicabilityConfig } from "../src/lib/model-applicability";
import { modelCapabilityConfigFor, type VideoCapabilityConfig } from "../src/lib/model-capabilities";
import type { ModelRequirements } from "../src/lib/model-selection";
import { systemChannelModelChannels } from "../src/lib/user-session";
import type { CapabilitySpec, PublicLogicalModelPriceTier } from "../src/services/api/logical-models";
import { createVideoGenerationsTask } from "../src/services/api/video-provider-newapi";
import { createSeedanceTask } from "../src/services/api/video-provider-seedance";
import type { VideoProviderDeps } from "../src/services/api/video-provider-deps";
import { videoResponseTools } from "../src/services/api/video-response";
import { defaultConfig, resolveModelRequestConfig, type AiConfig, type ModelChannel } from "../src/stores/use-config-store";

const model = "channel::cinema";
const emptyInput = { textCount: 1, imageCount: 0, videoCount: 0, audioCount: 0, characterCount: 0 };

function tier(overrides: Partial<PublicLogicalModelPriceTier> = {}): PublicLogicalModelPriceTier {
    return { selector: {}, resolution: "*", videoSeconds: 0, billingMode: "per_second", unitPriceMicrocredits: 1_000_000, inputTokenPriceMicrocredits: 0, outputTokenPriceMicrocredits: 0, cachedTokenPriceMicrocredits: 0, ...overrides };
}

function fixture(): AiConfig {
    const video: VideoCapabilityConfig = {
        references: { promptMaxChars: 100, minImages: 0, maxImages: 2, maxImageBytes: 10 * 1024 * 1024, maxVideos: 1, maxVideoBytes: 50 * 1024 * 1024, maxVideoDurationSeconds: 15, maxAudios: 1, maxAudioBytes: 5 * 1024 * 1024, maxAudioDurationSeconds: 10 },
        duration: { selection: "range", min: 1, max: 15, step: 1, default: 10 },
        ratios: ["16:9", "9:16"],
        defaultRatio: "16:9",
        resolutions: ["720p", "1080p"],
        defaultResolution: "720p",
        generateAudio: { supported: false, default: false },
        watermark: { supported: false, default: false },
        operations: ["text_to_video", "image_to_video", "reference_to_video", "audio_to_video"],
        defaultOperation: "text_to_video",
    };
    const channel: ModelChannel = {
        id: "channel", name: "测试渠道", baseUrl: "/api/channel", apiKey: "", apiFormat: "openai", scope: "system", enabled: true, models: ["cinema"],
        modelCosts: [{ model: "cinema", capability: "video", pricePolicy: "channel", billingMode: "per_second", unitPriceMicrocredits: 1_000_000, capabilityConfig: { version: 1, video }, logicalPriceTiers: [tier()] }],
    };
    return { ...structuredClone(defaultConfig), channels: [channel], models: [model], videoModels: [model], videoModel: model, model, videoSeconds: "6", vquality: "2160p", size: "1:1", videoGenerateAudio: "true", videoWatermark: "true", count: "3" };
}

function cost(config: AiConfig) {
    return config.channels[0].modelCosts![0];
}

function profile(config: AiConfig) {
    return cost(config).capabilityConfig!.video!;
}

function imageOnly(config: AiConfig, min = 2) {
    const video = profile(config);
    video.references.minImages = min;
    video.operations = ["image_to_video"];
    video.defaultOperation = "image_to_video";
}

function logicalSpec(overrides: Partial<CapabilitySpec> = {}): CapabilitySpec {
    return {
        version: 1, capability: "video", operations: ["text_to_video", "image_to_video"],
        inputs: { image: { min: 0, max: 2 }, video: { min: 0, max: 0 }, audio: { min: 0, max: 0 } },
        options: { videoSeconds: { values: [5, 10] }, vquality: { values: ["720p", "1080p"] }, size: { values: ["16:9", "9:16"] }, videoGenerateAudio: { values: [false] } },
        ...overrides,
    };
}

describe("video model applicability", () => {
    test("uses candidate defaults for readiness and request-local configuration", () => {
        const config = fixture();
        expect(assessModelApplicability(config, model)).toMatchObject({ status: "ready", estimatedCredits: 10 });
        expect(buildModelApplicabilityConfig(config, model)).toMatchObject({ model, videoSeconds: "10", vquality: "720p", size: "16:9", videoGenerateAudio: "false", videoWatermark: "false", count: "1" });
    });

    test("reports exactly how many images are missing, including character references", () => {
        const config = fixture();
        imageOnly(config);
        const result = assessModelApplicability(config, model, { input: { ...emptyInput, characterCount: 1 } });
        expect(result.status).toBe("needs_input");
        expect(result.reason).toContain("还需 1 张");
        expect(result.summary[1]).toContain("已用 1 / 上限 2");
        expect(result.summary[1]).toContain("最少 2");
        expect(assessModelApplicability(config, model, { input: { ...emptyInput, imageCount: 2 } }).status).toBe("ready");
    });

    test("suggests image-to-video only when no operation was requested", () => {
        const config = fixture();
        imageOnly(config, 0);
        expect(assessModelApplicability(config, model).status).toBe("needs_input");
        expect(assessModelApplicability(config, model, { videoOperation: "text_to_video" }).status).toBe("incompatible");
    });

    test("never fills missing images to reinterpret an explicit text-to-video request", () => {
        const config = fixture();
        imageOnly(config);
        expect(assessModelApplicability(config, model, { videoOperation: "text_to_video" }).status).toBe("incompatible");
    });

    test("an inferred operation remains inferable during missing-image rechecks", () => {
        const config = fixture();
        imageOnly(config, 1);
        expect(assessModelApplicability(config, model, { videoOperation: "text_to_video", videoOperationExplicit: false }).status).toBe("needs_input");
        expect(assessModelApplicability(config, model, { videoOperation: "text_to_video", videoOperationExplicit: true }).status).toBe("incompatible");
    });

    test("keeps an explicit unsupported operation even when images are present", () => {
        const config = fixture();
        imageOnly(config, 1);
        expect(assessModelApplicability(config, model, { videoOperation: "text_to_video", input: { ...emptyInput, imageCount: 1 } }).status).toBe("incompatible");
    });

    for (const [label, requirements] of [
        ["duration", { videoSeconds: "16" }],
        ["resolution", { options: { vquality: "2160p" } }],
        ["ratio", { options: { size: "1:1" } }],
        ["audio generation", { options: { videoGenerateAudio: true } }],
        ["watermark", { options: { videoWatermark: true } }],
        ["oversize image", { media: [{ kind: "image", bytes: 11 * 1024 * 1024 }] }],
        ["prompt", { prompt: "镜".repeat(101) }],
    ] satisfies Array<[string, ModelRequirements]>) {
        test(`missing images cannot hide incompatible ${label}`, () => {
            const config = fixture();
            imageOnly(config);
            const result = assessModelApplicability(config, model, requirements);
            expect(result.status).toBe("incompatible");
            expect(result.reason).not.toContain("还需");
        });
    }

    test("rechecks operation after filling images instead of checking minima alone", () => {
        const config = fixture();
        profile(config).references.minImages = 1;
        profile(config).operations = ["text_to_video"];
        expect(assessModelApplicability(config, model).status).toBe("incompatible");
    });

    test("counts media when an input summary is absent", () => {
        const config = fixture();
        imageOnly(config, 1);
        expect(assessModelApplicability(config, model, { media: [{ kind: "image" }] }).status).toBe("ready");
        expect(assessModelApplicability(config, model, { media: [{ kind: "image" }, { kind: "image" }, { kind: "image" }] }).status).toBe("incompatible");
    });

    test("validates known video/audio metadata without treating unknown metadata as zero", () => {
        const config = fixture();
        for (const media of [
            [{ kind: "video" as const, durationSeconds: 16 }],
            [{ kind: "audio" as const, durationSeconds: 11 }],
            [{ kind: "video" as const, bytes: 51 * 1024 * 1024 }],
            [{ kind: "audio" as const, bytes: 6 * 1024 * 1024 }],
        ]) expect(assessModelApplicability(config, model, { media }).status).toBe("incompatible");
        expect(assessModelApplicability(config, model, { media: [{ kind: "video" }] }).status).toBe("ready");
    });

    test("has no fallback claims when the model does not declare a video profile", () => {
        const config = fixture();
        cost(config).capabilityConfig = undefined;
        const result = assessModelApplicability(config, model);
        expect(result.status).toBe("unknown");
        expect(result.estimatedCredits).toBeUndefined();
        expect(result.summary.join(" ")).not.toMatch(/720|1080|16:9|15 秒|8000/);
    });

    test("public catalogue entries without a capability config stay unknown after the real session mapping", () => {
        const config = fixture();
        config.channels = systemChannelModelChannels([{
            id: "channel", name: "目录渠道", displayName: "目录渠道",
            models: [{ id: "catalogue-model", modelKey: "cinema", displayName: "Cinema", icon: "", capability: "video", available: true, pricingMode: "provider", priceLabel: "", priceTiers: [{ ...tier(), id: "catalogue-tier" }] }],
        }]);
        expect(cost(config).capabilityConfig).toBeUndefined();
        // Legacy controls may still use their fallback, but applicability must not.
        expect(modelCapabilityConfigFor(config, model).video).toBeDefined();
        expect(assessModelApplicability(config, model).status).toBe("unknown");
        expect(assessModelApplicability(config, model).summary.join(" ")).not.toMatch(/720|1080|16:9|15 秒|8000/);
    });

    for (const field of ["operations", "duration", "resolutions", "ratios", "references"] as const) {
        test(`an incomplete raw profile without ${field} is unknown, not a fallback match`, () => {
            const config = fixture();
            delete (profile(config) as unknown as Record<string, unknown>)[field];
            expect(assessModelApplicability(config, model).status).toBe("unknown");
        });
    }

    test("an incomplete reference declaration is unknown", () => {
        const config = fixture();
        delete (profile(config).references as Partial<VideoCapabilityConfig["references"]>).maxVideos;
        expect(assessModelApplicability(config, model).status).toBe("unknown");
    });

    test("rejects disabled, absent and removed video catalogue entries", () => {
        const config = fixture();
        config.channels[0].enabled = false;
        expect(assessModelApplicability(config, model).status).toBe("incompatible");
        config.channels[0].enabled = true;
        config.videoModels = [];
        expect(assessModelApplicability(config, model).status).toBe("incompatible");
        config.videoModels = [model];
        config.channels[0].models = [];
        expect(assessModelApplicability(config, model).status).toBe("incompatible");
        expect(assessModelApplicability(fixture(), "absent::cinema").status).toBe("incompatible");
    });

    test("keeps constraints and prices independent for the same model in two channels", () => {
        const config = fixture();
        const second = structuredClone(config.channels[0]);
        second.id = "second";
        second.modelCosts![0].capabilityConfig!.video!.references.maxImages = 1;
        second.modelCosts![0].logicalPriceTiers = [tier({ unitPriceMicrocredits: 2_000_000 })];
        config.channels.push(second);
        config.models.push("second::cinema");
        config.videoModels.push("second::cinema");
        const requirements = { input: { ...emptyInput, imageCount: 2 } };
        expect(assessModelApplicability(config, model, requirements)).toMatchObject({ status: "ready", estimatedCredits: 10 });
        expect(assessModelApplicability(config, "second::cinema", requirements).status).toBe("incompatible");
        expect(assessModelApplicability(config, "second::cinema").estimatedCredits).toBe(20);
    });

    test("orders the summary for the picker and does not claim two images prove first/last frames", () => {
        const result = assessModelApplicability(fixture(), model, { input: { ...emptyInput, imageCount: 2 }, prompt: "镜头" });
        expect(result.summary.slice(0, 6).map((line) => line.split("：")[0])).toEqual(["模式", "图片", "视频", "音频", "时长", "比例"]);
        const text = result.summary.join("\n");
        expect(text).toContain("目录未声明首尾帧边界");
        expect(text).toContain("不代表成片一定有声或无声");
        expect(text).toContain("图片 ≤ 10 MB");
        expect(text).toContain("视频 ≤ 15 秒");
        expect(text).toContain("音频 ≤ 10 秒");
        expect(text).toContain("已用 2 / 上限 100 字符");
    });

    test("does not mutate shared config or requirements", () => {
        const config = fixture();
        imageOnly(config);
        const requirements: ModelRequirements = { input: { ...emptyInput, imageCount: 1 }, options: { videoSeconds: 5 }, media: [{ kind: "image" }] };
        const before = structuredClone({ config, requirements });
        assessModelApplicability(config, model, requirements);
        buildModelApplicabilityConfig(config, model, requirements);
        expect({ config, requirements }).toEqual(before);
    });
});

describe("applicability exact-SKU pricing", () => {
    test("compares one-second rates with fixed ten-second request totals, not unit prices", () => {
        const perSecond = fixture();
        const fixed = fixture();
        cost(fixed).logicalPriceTiers = [tier({ billingMode: "fixed_request", unitPriceMicrocredits: 8_000_000, selector: { videoSeconds: "10" } })];
        expect(assessModelApplicability(perSecond, model, { videoSeconds: "10" }).estimatedCredits).toBe(10);
        expect(assessModelApplicability(fixed, model, { videoSeconds: "10" }).estimatedCredits).toBe(8);
        expect(assessModelApplicability(perSecond, model, { videoSeconds: "1" }).estimatedCredits).toBe(1);
    });

    test("does not multiply fixed video prices by a stale image count or by seconds", () => {
        const config = fixture();
        cost(config).logicalPriceTiers = [tier({ billingMode: "fixed_request", unitPriceMicrocredits: 2_000_000 })];
        expect(assessModelApplicability(config, model, { videoSeconds: "10", options: { count: 7 } }).estimatedCredits).toBe(2);
    });

    test("keeps fractional per-second totals", () => {
        const config = fixture();
        profile(config).duration = { selection: "range", min: 1, max: 15, step: 0.5, default: 1.5 };
        expect(assessModelApplicability(config, model).estimatedCredits).toBe(1.5);
    });

    test("blocks a configured system model with no matching SKU, rather than reusing a scalar price", () => {
        const config = fixture();
        cost(config).logicalPriceTiers = [tier({ selector: { videoSeconds: "5" } })];
        const result = assessModelApplicability(config, model, { videoSeconds: "10" });
        expect(result.status).toBe("incompatible");
        expect(result.reason).toContain("售价");
        expect(result.estimatedCredits).toBeUndefined();
    });

    test("a needs-input suggestion must also have a price for the filled request", () => {
        const config = fixture();
        imageOnly(config, 1);
        cost(config).logicalPriceTiers = [tier({ selector: { operation: "text_to_video" } })];
        expect(assessModelApplicability(config, model).status).toBe("incompatible");
        cost(config).logicalPriceTiers = [tier({ selector: { operation: "image_to_video", imageCount: "1" } })];
        expect(assessModelApplicability(config, model)).toMatchObject({ status: "needs_input", estimatedCredits: 10 });
    });

    test("token pricing remains unknown rather than impersonating a free request", () => {
        const config = fixture();
        cost(config).logicalPriceTiers = [tier({ billingMode: "token", unitPriceMicrocredits: 0, outputTokenPriceMicrocredits: 5_000_000 })];
        expect(assessModelApplicability(config, model)).toMatchObject({ status: "ready" });
        expect(assessModelApplicability(config, model).estimatedCredits).toBeUndefined();
    });

    test("different equally specific prices are ambiguous, equal prices are not", () => {
        const config = fixture();
        cost(config).logicalPriceTiers = [tier(), tier({ unitPriceMicrocredits: 2_000_000 })];
        expect(assessModelApplicability(config, model).estimatedCredits).toBeUndefined();
        cost(config).logicalPriceTiers = [tier(), tier()];
        expect(assessModelApplicability(config, model).estimatedCredits).toBe(10);
    });

    test("explicit free prices are zero, invalid and absent prices are not", () => {
        const config = fixture();
        cost(config).logicalPriceTiers = [tier({ unitPriceMicrocredits: 0 })];
        expect(assessModelApplicability(config, model).estimatedCredits).toBe(0);
        for (const invalid of [-1, Number.NaN, Number.POSITIVE_INFINITY]) {
            cost(config).logicalPriceTiers = [tier({ unitPriceMicrocredits: invalid })];
            expect(assessModelApplicability(config, model)).toMatchObject({ status: "incompatible" });
            expect(assessModelApplicability(config, model).estimatedCredits).toBeUndefined();
        }
        cost(config).logicalPriceTiers = [];
        expect(assessModelApplicability(config, model).status).toBe("incompatible");
    });

    test("personal channels without an explicit pricing policy have an unknown price but stay usable", () => {
        const config = fixture();
        config.channels[0].scope = "user";
        cost(config).pricePolicy = undefined;
        cost(config).unitPriceMicrocredits = 0;
        cost(config).logicalPriceTiers = [];
        expect(assessModelApplicability(config, model).status).toBe("ready");
        expect(assessModelApplicability(config, model).estimatedCredits).toBeUndefined();
    });

    test("partial requirements retain each candidate's own duration, resolution, ratio and audio defaults", () => {
        const config = fixture();
        profile(config).generateAudio = { supported: true, default: true };
        cost(config).logicalPriceTiers = [tier({ selector: { videoSeconds: "10", vquality: "720p", videoGenerateAudio: "true" }, unitPriceMicrocredits: 2_000_000 })];
        expect(assessModelApplicability(config, model, { options: { size: "9:16" } })).toMatchObject({ status: "ready", estimatedCredits: 20 });
        const second = structuredClone(config.channels[0]);
        second.id = "second";
        const secondCost = second.modelCosts![0];
        secondCost.capabilityConfig!.video!.duration.default = 5;
        secondCost.capabilityConfig!.video!.defaultResolution = "1080p";
        secondCost.capabilityConfig!.video!.generateAudio.default = false;
        secondCost.logicalPriceTiers = [tier({ selector: { videoSeconds: "5", vquality: "1080p", videoGenerateAudio: "false" }, unitPriceMicrocredits: 3_000_000 })];
        config.channels.push(second);
        config.videoModels.push("second::cinema");
        config.models.push("second::cinema");
        expect(assessModelApplicability(config, "second::cinema", { options: { size: "9:16" } })).toMatchObject({ status: "ready", estimatedCredits: 15 });
    });

    test("explicit aliases and top-level duration override defaults without being normalized away", () => {
        const config = fixture();
        expect(buildModelApplicabilityConfig(config, model, { videoSeconds: "5", options: { videoSeconds: 10, resolution: "1080p", aspectRatio: "9:16" } })).toMatchObject({ videoSeconds: "5", vquality: "1080p", size: "9:16" });
        expect(assessModelApplicability(config, model, { options: { resolution: "2160p" } }).status).toBe("incompatible");
    });
});

describe("declared logical video profiles", () => {
    test("uses logical defaults without a raw video profile or global fallback", () => {
        const config = fixture();
        cost(config).capabilityConfig = undefined;
        cost(config).logicalCapabilitySpec = logicalSpec();
        cost(config).defaultOptions = { videoSeconds: 10, vquality: "1080p", size: "9:16", videoGenerateAudio: false };
        cost(config).logicalPriceTiers = [tier({ selector: { vquality: "1080p", videoSeconds: "10", videoGenerateAudio: "false" } })];
        expect(assessModelApplicability(config, model, { options: { size: "16:9" } })).toMatchObject({ status: "ready", estimatedCredits: 10 });
        expect(assessModelApplicability(config, model).summary.join(" ")).not.toContain("单文件");
    });

    test("logical minImages becomes needs-input only when the entire filled profile matches", () => {
        const config = fixture();
        cost(config).capabilityConfig = undefined;
        cost(config).logicalCapabilitySpec = logicalSpec({ operations: ["image_to_video"], inputs: { image: { min: 2, max: 2 } } });
        expect(assessModelApplicability(config, model, { input: { ...emptyInput, imageCount: 1 } }).status).toBe("needs_input");
        expect(assessModelApplicability(config, model, { options: { vquality: "2160p" } }).status).toBe("incompatible");
    });

    test("validates profiles-only option constraints rather than filtering all options away", () => {
        const config = fixture();
        cost(config).capabilityConfig = undefined;
        cost(config).logicalCapabilityProfiles = [logicalSpec()];
        expect(assessModelApplicability(config, model).status).toBe("ready");
        expect(assessModelApplicability(config, model, { options: { vquality: "2160p" } }).status).toBe("incompatible");
    });

    for (const field of ["videoGenerateAudio", "videoWatermark"]) {
        test(`does not silently discard an enabled undeclared ${field} switch`, () => {
            const config = fixture();
            cost(config).capabilityConfig = undefined;
            const spec = logicalSpec();
            delete spec.options![field];
            for (const profilesOnly of [false, true]) {
                cost(config).logicalCapabilitySpec = profilesOnly ? undefined : spec;
                cost(config).logicalCapabilityProfiles = profilesOnly ? [spec] : undefined;
                for (const value of [true, "true"]) {
                    const result = assessModelApplicability(config, model, { options: { [field]: value } });
                    expect(result.status).toBe("incompatible");
                    expect(result.reason).toContain("目录未声明");
                    expect(result.estimatedCredits).toBeUndefined();
                }
                for (const value of [undefined, false, "false"]) {
                    expect(assessModelApplicability(config, model, { options: { [field]: value } }).status).toBe("ready");
                }
            }
        });
    }

    test("a declared audio switch may be enabled, without leaking a hidden route's switch", () => {
        const config = fixture();
        cost(config).capabilityConfig = undefined;
        const spec = logicalSpec();
        spec.options!.videoGenerateAudio = { values: [false, true] };
        cost(config).logicalCapabilitySpec = spec;
        expect(assessModelApplicability(config, model, { options: { videoGenerateAudio: true } }).status).toBe("ready");
        cost(config).logicalCapabilityProfiles = [structuredClone(spec)];
        delete spec.options!.videoGenerateAudio;
        expect(assessModelApplicability(config, model, { options: { videoGenerateAudio: true } }).status).toBe("incompatible");
        expect(assessModelApplicability(config, model, { options: { videoGenerateAudio: false } }).status).toBe("ready");
    });

    test("must not combine input and option limits from different logical profiles", () => {
        const config = fixture();
        cost(config).capabilityConfig = undefined;
        const oneImage = logicalSpec({ inputs: { image: { min: 1, max: 1 } }, operations: ["image_to_video"], options: { ...logicalSpec().options, videoSeconds: { values: [5] }, vquality: { values: ["720p"] } } });
        const twoImages = logicalSpec({ inputs: { image: { min: 2, max: 2 } }, operations: ["image_to_video"], options: { ...logicalSpec().options, videoSeconds: { values: [10] }, vquality: { values: ["1080p"] } } });
        cost(config).logicalCapabilitySpec = logicalSpec();
        cost(config).logicalCapabilityProfiles = [oneImage, twoImages];
        const incompatible = assessModelApplicability(config, model, { videoSeconds: "5", options: { vquality: "1080p" } });
        expect(incompatible.status).toBe("incompatible");
        expect(assessModelApplicability(config, model, { videoSeconds: "10", options: { vquality: "1080p" } }).status).toBe("needs_input");
    });

    test("non-video assessment delegates existing compatibility without adding video behaviour", () => {
        const result = assessModelApplicability(fixture(), model, { capability: "text", input: { ...emptyInput, audioCount: 1 } });
        expect(result.status).toBe("incompatible");
        expect(result.reason).toContain("参考音频");
        expect(result.summary).toEqual([]);
    });
});

describe("declared adapter combination constraints", () => {
    const combinations = [
        { name: "text only", imageCount: 0, videoCount: 0, audioCount: 0, characterCount: 0 },
        { name: "image only", imageCount: 1, videoCount: 0, audioCount: 0, characterCount: 0 },
        { name: "video only", imageCount: 0, videoCount: 1, audioCount: 0, characterCount: 0 },
        { name: "audio only", imageCount: 0, videoCount: 0, audioCount: 1, characterCount: 0 },
        { name: "image and video", imageCount: 1, videoCount: 1, audioCount: 0, characterCount: 0 },
        { name: "image and audio", imageCount: 1, videoCount: 0, audioCount: 1, characterCount: 0 },
        { name: "video and audio", imageCount: 0, videoCount: 1, audioCount: 1, characterCount: 0 },
        { name: "image, video and audio", imageCount: 1, videoCount: 1, audioCount: 1, characterCount: 0 },
        { name: "character and audio", imageCount: 0, videoCount: 0, audioCount: 1, characterCount: 1 },
    ];

    for (const protocol of ["volcengine-ark-video", "volcengine-ark-agent-plan-video", "newapi-channel-2"]) {
        for (const { name, ...counts } of combinations) {
            test(`${protocol}: ${name}`, () => {
                const config = fixture();
                cost(config).protocol = protocol;
                const rejected = counts.audioCount > 0 && (protocol === "newapi-channel-2" ? counts.videoCount === 0 : counts.videoCount + counts.imageCount + counts.characterCount === 0);
                const result = assessModelApplicability(config, model, { input: { textCount: 1, ...counts }, prompt: "跟随节奏" });
                expect(result.status).toBe(rejected ? "incompatible" : "ready");
                if (rejected) {
                    expect(result.reason).toContain(protocol === "newapi-channel-2" ? "至少 1 个参考视频" : "不支持纯音频或文本+音频");
                    expect(result.estimatedCredits).toBeUndefined();
                } else {
                    expect(result.estimatedCredits).toBe(10);
                }
            });
        }
    }

    test("known adapter rejection also applies to media-only requirements and an explicit audio mode", () => {
        for (const protocol of ["volcengine-ark-video", "volcengine-ark-agent-plan-video", "newapi-channel-2"]) {
            const config = fixture();
            cost(config).protocol = protocol;
            for (const prompt of [undefined, "跟随节奏"]) {
                expect(assessModelApplicability(config, model, { prompt, media: [{ kind: "audio" }], videoOperation: "audio_to_video" }).status).toBe("incompatible");
            }
        }
    });

    test("NewAPI still requires video after the minimum number of images is filled", () => {
        const config = fixture();
        cost(config).protocol = "newapi-channel-2";
        profile(config).references.minImages = 1;
        const result = assessModelApplicability(config, model, { input: { ...emptyInput, audioCount: 1 } });
        expect(result.status).toBe("incompatible");
        expect(result.reason).toContain("至少 1 个参考视频");
        expect(result.estimatedCredits).toBeUndefined();
        expect(result.summary.join(" ")).toContain("参考音频必须同时提供至少 1 个参考视频");
    });

    test("Ark can suggest declared missing images only after the whole filled request passes", () => {
        const config = fixture();
        cost(config).protocol = "volcengine-ark-video";
        profile(config).references.minImages = 1;
        expect(assessModelApplicability(config, model, { input: { ...emptyInput, audioCount: 1 } })).toMatchObject({ status: "needs_input", estimatedCredits: 10 });
        expect(assessModelApplicability(config, model, { input: { ...emptyInput, audioCount: 1 }, options: { vquality: "2160p" } }).status).toBe("incompatible");
        expect(assessModelApplicability(config, model, { input: { ...emptyInput, audioCount: 1 }, videoOperation: "audio_to_video" }).status).toBe("incompatible");
    });

    test("allowed adapter combinations must still satisfy declared capabilities", () => {
        const config = fixture();
        cost(config).protocol = "volcengine-ark-video";
        profile(config).references.maxAudios = 0;
        expect(assessModelApplicability(config, model, { input: { ...emptyInput, imageCount: 1, audioCount: 1 } }).status).toBe("incompatible");
        cost(config).protocol = "newapi-channel-2";
        profile(config).references.maxAudios = 1;
        profile(config).references.maxVideos = 0;
        expect(assessModelApplicability(config, model, { input: { ...emptyInput, videoCount: 1, audioCount: 1 } }).status).toBe("incompatible");
    });

    test("uses a personal channel's declared transport, but a per-model protocol takes precedence", () => {
        const config = fixture();
        config.channels[0].scope = "user";
        config.channels[0].interfaceType = "newapi-channel-2";
        const requirements = { input: { ...emptyInput, imageCount: 1, audioCount: 1 } };
        expect(assessModelApplicability(config, model, requirements).status).toBe("incompatible");
        cost(config).protocol = "volcengine-ark-video";
        expect(assessModelApplicability(config, model, requirements).status).toBe("ready");
        config.channels[0].interfaceType = "volcengine-ark-video";
        cost(config).protocol = "newapi-channel-2";
        expect(assessModelApplicability(config, model, requirements).status).toBe("incompatible");
        cost(config).protocol = "custom-audio-video";
        expect(assessModelApplicability(config, model, { input: { ...emptyInput, audioCount: 1 } }).status).toBe("ready");
    });

    test("does not infer hidden system route protocols from channel defaults, model names or URLs", () => {
        const config = fixture();
        cost(config).logicalModelId = "family";
        cost(config).capabilityConfig = undefined;
        cost(config).logicalCapabilitySpec = logicalSpec({
            operations: ["audio_to_video"],
            inputs: { audio: { min: 0, max: 1 } },
        });
        config.channels[0].interfaceType = "newapi-channel-2";
        config.channels[0].baseUrl = "https://ark.cn-beijing.volces.com/api/plan/v3";
        cost(config).displayName = "Seedance";
        cost(config).model = "doubao-seedance-2.0";
        config.channels[0].models = [cost(config).model];
        const selected = `channel::${cost(config).model}`;
        config.model = config.videoModel = selected;
        config.models = config.videoModels = [selected];
        const requirements = { input: { ...emptyInput, audioCount: 1 } };
        const hidden = assessModelApplicability(config, selected, requirements);
        expect(hidden.status).toBe("ready");
        expect(hidden.summary.join(" ")).not.toContain("组合约束");
        cost(config).protocol = "volcengine-ark-video";
        expect(assessModelApplicability(config, selected, requirements).status).toBe("incompatible");
    });

    test("does not spread one NewAPI protocol's restriction to another protocol or an undeclared model", () => {
        const config = fixture();
        for (const protocol of [undefined, "newapi-channel-1", "minimax-video", "custom-audio-video"]) {
            cost(config).protocol = protocol;
            expect(assessModelApplicability(config, model, { input: { ...emptyInput, audioCount: 1 } }).status).toBe("ready");
        }
        cost(config).protocol = "volcengine-ark-video";
        cost(config).capabilityConfig = undefined;
        expect(assessModelApplicability(config, model, { input: { ...emptyInput, audioCount: 1 } }).status).toBe("unknown");
    });

    test("the same model in different channels inherits only that channel's explicit protocol", () => {
        const config = fixture();
        cost(config).protocol = "volcengine-ark-video";
        const second = structuredClone(config.channels[0]);
        second.id = "second";
        second.modelCosts![0].protocol = "newapi-channel-2";
        config.channels.push(second);
        config.models.push("second::cinema");
        config.videoModels.push("second::cinema");
        const requirements = { input: { ...emptyInput, imageCount: 1, audioCount: 1 } };
        expect(assessModelApplicability(config, model, requirements).status).toBe("ready");
        expect(assessModelApplicability(config, "second::cinema", requirements).status).toBe("incompatible");
    });

    test("rejection reasons agree with the real adapters before any transport can run", async () => {
        let transportCalls = 0;
        const deps = {
            transport: { post: async () => { transportCalls += 1; throw new Error("Network is forbidden in this test"); } },
            response: videoResponseTools,
        } as unknown as VideoProviderDeps;
        const audio = { id: "audio", name: "audio.mp3", type: "audio/mpeg", url: "https://example.invalid/audio.mp3" };
        const image = { id: "image", name: "image.png", type: "image/png", dataUrl: "", url: "https://example.invalid/image.png" };
        for (const protocol of ["volcengine-ark-video", "volcengine-ark-agent-plan-video", "newapi-channel-2"]) {
            const config = fixture();
            cost(config).protocol = protocol;
            const images = protocol === "newapi-channel-2" ? [image] : [];
            const requirements = { input: { ...emptyInput, imageCount: images.length, audioCount: 1 } };
            const assessment = assessModelApplicability(config, model, requirements);
            const create = protocol === "newapi-channel-2" ? createVideoGenerationsTask : createSeedanceTask;
            expect(assessment.status).toBe("incompatible");
            await expect(create(deps, resolveModelRequestConfig(config, model), model, "跟随节奏", images, [], [audio])).rejects.toThrow(assessment.reason);
        }
        expect(transportCalls).toBe(0);
    });
});
