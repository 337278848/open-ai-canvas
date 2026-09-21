import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ModelCapabilityHint } from "../src/components/model-capability-hint";
import { ModelPicker } from "../src/components/model-picker";
import { VideoModelOptions } from "../src/components/video-model-options";
import { defaultModelCapabilityConfig } from "../src/lib/model-capabilities";
import type { ModelRequirements } from "../src/lib/model-selection";
import { defaultConfig, normalizeConfigSnapshot, type ModelChannel } from "../src/stores/use-config-store";

function fixture() {
    const channel: ModelChannel = { id: "video", name: "独立渠道", baseUrl: "/api/video", apiKey: "system", apiFormat: "openai", scope: "system", enabled: true,
        models: ["per-second", "fixed-price", "1080-only", "missing-capabilities"],
        modelCosts: ["per-second", "fixed-price", "1080-only", "missing-capabilities"].map((model) => {
            const profile = defaultModelCapabilityConfig();
            profile.video!.resolutions = model === "1080-only" ? ["1080p"] : ["720p"];
            profile.video!.references.maxImages = 2;
            return { model, displayName: model, capability: "video", capabilityConfig: model === "missing-capabilities" ? undefined : profile,
                billingMode: model === "per-second" ? "per_second" : "fixed_request", unitPriceMicrocredits: model === "per-second" ? 80_000 : 200_000 };
        }),
    };
    const config = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [channel], count: "3" } }).config;
    const requirements: ModelRequirements = { capability: "video", videoSeconds: "10", options: { vquality: "720", size: "16:9" },
        input: { textCount: 1, imageCount: 0, videoCount: 0, audioCount: 0, characterCount: 0 } };
    return { config, requirements };
}

describe("in-context video model boundaries", () => {
    test("video picker retains an explicitly selected same-name personal model and its price", () => {
        const { config, requirements } = fixture();
        config.channels[0].scope = "user";
        config.channels[0].modelCosts!.forEach((cost) => { cost.displayName = "同一系列"; });
        const html = renderToStaticMarkup(<ModelPicker config={config} value="video::fixed-price" capability="video" requirements={requirements} showSelectedPrice onChange={() => {}} />);
        expect(html).toContain("0.2/次");
        expect(html).not.toContain("0.08/秒");
    });

    test("default list hides incompatible and unknown models, and sorts by total rather than unit price", () => {
        const { config, requirements } = fixture();
        const html = renderToStaticMarkup(<VideoModelOptions config={config} models={config.videoModels} current="video::per-second" requirements={requirements} showPrices onSelect={() => {}} />);
        expect(html).toContain("查看全部");
        expect(html).toContain("推荐");
        expect(html).not.toContain("1080-only");
        expect(html).not.toContain("missing-capabilities");
        expect(html.indexOf("fixed-price")).toBeLessThan(html.indexOf("per-second"));
        expect(html).toContain("0.8");
        expect(html).toContain("0.2");
        expect(html).toContain("积分");
        expect(html).not.toContain("按本次预计总价从低到高排列");
        expect(html).not.toContain("固定时长与清晰度可同规格比价");
    });

    test("no compatible model keeps an explanatory escape instead of an empty menu", () => {
        const { config, requirements } = fixture();
        const html = renderToStaticMarkup(<VideoModelOptions config={config} models={config.videoModels} current="" requirements={{ ...requirements, videoSeconds: "99" }} showPrices onSelect={() => {}} />);
        expect(html).toContain("没有满足当前全部条件");
        expect(html).toContain("素材与规格已保留");
        expect(html).toContain("查看全部模型及原因");
    });

    test("selected model always exposes boundaries and the rejection reason without opening the picker", () => {
        const { config, requirements } = fixture();
        const html = renderToStaticMarkup(<ModelCapabilityHint config={config} model="video::1080-only" requirements={requirements} />);
        expect(html).toContain('aria-label="当前模型能力"');
        expect(html).toContain("当前条件不适用");
        expect(html).toContain("分辨率");
        expect(html).toContain("完整能力与限制");
        expect(html).not.toContain('title="当前条件不适用"');
    });
});
