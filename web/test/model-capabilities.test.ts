import assert from "node:assert/strict";
import test from "node:test";

// Bun 直接执行 TypeScript 测试时需要保留扩展名；生产 tsconfig 不包含 test/。
import { DEFAULT_VIDEO_PROMPT_MAX_CHARS, defaultModelCapabilityConfig, modelCapabilityConfigFor, normalizeVideoValue } from "../src/lib/model-capabilities.ts";

test("text multimodal capability is not guessed from a model name", () => {
    for (const model of ["gpt-4o", "gemini-2.5-pro", "doubao-seed"]) {
        const text = defaultModelCapabilityConfig(undefined, model).text!;
        assert.equal(text.references.maxImages, 0);
        assert.equal(text.references.maxVideos, 0);
    }
});

test("switching to MiniMax H3 replaces an unsupported 720p value with 768P", () => {
    const profile = defaultModelCapabilityConfig("minimax-video", "MiniMax-H3").video!;

    assert.deepEqual(normalizeVideoValue(profile, { seconds: "11", ratio: "16:9", resolution: "720" }), {
        seconds: "11",
        ratio: "16:9",
        resolution: "768P",
    });
});

test("LXMone H3 workflow applies limits per model instead of narrowing every H3 SKU", () => {
    const h3a = defaultModelCapabilityConfig("lxmone-h3-workflow", "minimax-h3-a").video!;
    const h3e = defaultModelCapabilityConfig("lxmone-h3-workflow", "minimax-h3-e").video!;
    const h3b = defaultModelCapabilityConfig("lxmone-h3-workflow", "minimax-h3-b").video!;

    assert.equal(h3a.duration.max, 12);
    assert.equal(h3e.references.maxImages, 1);
    assert.equal(h3b.duration.max, 15);
    assert.equal(h3b.references.maxImages, 9);
});

test("persisted legacy LXMone H3 capability is capped after frontend merge", () => {
    const profile = modelCapabilityConfigFor(
        {
            channels: [
                {
                    id: "gravity",
                    models: ["minimax-h3-a"],
                    modelCosts: [
                        {
                            model: "minimax-h3-a",
                            protocol: "lxmone-h3-workflow",
                            capabilityConfig: {
                                version: 1,
                                video: {
                                    ...defaultModelCapabilityConfig("lxmone-h3-workflow", "minimax-h3-a").video!,
                                    duration: { selection: "range", min: 1, max: 15, step: 1, default: 15 },
                                },
                            },
                        },
                    ],
                },
            ],
        },
        "gravity::minimax-h3-a",
    ).video!;
    assert.equal(profile.duration.max, 12);
    assert.equal(profile.duration.default, 12);
});

test("LXMone H3 limits survive a models/ prefixed channel selection", () => {
    const profile = modelCapabilityConfigFor(
        {
            channels: [
                {
                    id: "gravity",
                    models: ["minimax-h3-a"],
                    modelCosts: [{ model: "minimax-h3-a", protocol: "lxmone-h3-workflow" }],
                },
            ],
        },
        "gravity::models/minimax-h3-a",
    ).video!;
    assert.equal(profile.duration.max, 12);
});

// 视频提示词由「输入框文本 + 连线内容 + 技能上下文」合成，技能上下文预算为 32000，
// 合成结果远长于用户手输内容。默认上限过小会把正常可用的画布工作流拦在本地预检。
// 这里锁定默认值本身，避免被改回偏小值（前端放行/后端拒绝的判定必须同源）。
test("video prompt default allows a composed canvas prompt", () => {
    assert.equal(DEFAULT_VIDEO_PROMPT_MAX_CHARS, 8000);
    for (const protocol of [undefined, "seedance-videos-compatible", "agnes-video", "volcengine-ark-video"]) {
        const profile = defaultModelCapabilityConfig(protocol, "test-model");
        assert.equal(profile.video!.references.promptMaxChars, DEFAULT_VIDEO_PROMPT_MAX_CHARS);
    }
});

test("raising the video default leaves text and image limits untouched", () => {
    // 只放宽视频默认值，避免顺带改变其它能力的判定口径。
    const profile = defaultModelCapabilityConfig("seedance-videos-compatible", "sd-2.5");
    assert.equal(profile.text!.references.promptMaxChars, 32000);
    assert.equal(profile.image!.references.promptMaxChars, 32000);
});
