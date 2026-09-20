import { describe, expect, test } from "bun:test";
import { defaultModelCapabilityConfig } from "../src/lib/model-capabilities";
import { modelCompatibilityError, type ModelRequirements } from "../src/lib/model-selection";
import { defaultConfig, type AiConfig } from "../src/stores/use-config-store";

function fixture() {
    const capabilityConfig = defaultModelCapabilityConfig();
    const video = capabilityConfig.video!;
    video.references = { ...video.references, minImages: 2, maxImages: 2, maxVideos: 1, maxAudios: 1, maxVideoBytes: 10 * 1024 * 1024, maxAudioBytes: 1024 * 1024, maxVideoDurationSeconds: 5, maxAudioDurationSeconds: 10 };
    video.duration = { selection: "range", min: 4, max: 10, step: 2, default: 6 };
    video.ratios = ["16:9", "9:16"];
    video.resolutions = ["720p", "2K"];
    video.operations = ["image_to_video", "reference_to_video"];
    const config: AiConfig = { ...defaultConfig, channels: [{
        id: "test", name: "Test", baseUrl: "/api/test", apiKey: "system", apiFormat: "openai", scope: "system",
        models: ["video-test"], modelCosts: [{ model: "video-test", capability: "video", billingMode: "per_second", unitPriceMicrocredits: 1, capabilityConfig }],
    }] };
    const requirements: ModelRequirements = { capability: "video", input: { textCount: 1, imageCount: 2, videoCount: 0, audioCount: 0, characterCount: 0 }, videoSeconds: "6", options: { vquality: "720", size: "16:9", videoGenerateAudio: false, videoWatermark: false } };
    return { config, requirements, video };
}

describe("video compatibility uses the actual request constraints", () => {
    test("checks minimum references without merging image, video and audio capacities", () => {
        const { config, requirements } = fixture();
        expect(modelCompatibilityError(config, "test::video-test", { ...requirements, input: { ...requirements.input!, imageCount: 1 } })).toContain("至少需要 2 张");
        expect(modelCompatibilityError(config, "test::video-test", { ...requirements, input: { ...requirements.input!, audioCount: 1 } })).toBe("");
        expect(modelCompatibilityError(config, "test::video-test", { ...requirements, input: { ...requirements.input!, videoCount: 2 } })).toContain("最多支持 1 个参考视频");
    });

    test("explicit video mode is respected without changing legacy connected-input inference", () => {
        const { config, requirements } = fixture();
        const legacy = { ...requirements, videoOperation: "text_to_video" };
        expect(modelCompatibilityError(config, "test::video-test", legacy)).toBe("");
        expect(modelCompatibilityError(config, "test::video-test", { ...legacy, videoOperationExplicit: true })).toContain("文生视频");
    });

    test("checks resolution, ratio, output switches and duration even with no input summary", () => {
        const { config, requirements } = fixture();
        for (const [options, error] of [
            [{ vquality: "1080P" }, "分辨率"],
            [{ size: "1:1" }, "画面比例"],
            [{ videoGenerateAudio: "true" }, "生成声音"],
            [{ videoWatermark: true }, "水印"],
            [{ videoSeconds: 7 }, "视频时长"],
        ] as const) {
            expect(modelCompatibilityError(config, "test::video-test", { ...requirements, input: undefined, videoSeconds: undefined, options })).toContain(error);
        }
    });

    test("does not regress legacy resolution aliases or pixel-size ratios", () => {
        const { config, requirements } = fixture();
        for (const resolution of ["720", "720P", "2k", "1440p", "auto"]) {
            expect(modelCompatibilityError(config, "test::video-test", { ...requirements, options: { size: "1280×720", vquality: resolution } })).toBe("");
        }
    });

    test("checks known file size, reference duration and prompt length without inventing missing metadata", () => {
        const { config, requirements, video } = fixture();
        expect(modelCompatibilityError(config, "test::video-test", { ...requirements, media: [{ kind: "video", bytes: 11 * 1024 * 1024 }] })).toContain("10 MB");
        expect(modelCompatibilityError(config, "test::video-test", { ...requirements, media: [{ kind: "video", durationSeconds: 5.1 }] })).toContain("5 秒");
        expect(modelCompatibilityError(config, "test::video-test", { ...requirements, media: [{ kind: "audio", durationSeconds: 11 }] })).toContain("10 秒");
        expect(modelCompatibilityError(config, "test::video-test", { ...requirements, media: [{ kind: "video" }] })).toBe("");
        video.references.promptMaxChars = 3;
        expect(modelCompatibilityError(config, "test::video-test", { ...requirements, prompt: "四个汉字" })).toContain("最多 3 个字符");
    });
});
