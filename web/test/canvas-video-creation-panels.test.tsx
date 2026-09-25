import { afterEach, describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { CanvasNodePromptPanel, buildNodeConfig } from "../src/components/canvas/canvas-node-prompt-panel";
import { CanvasConfigNodePanel } from "../src/components/canvas/canvas-config-node-panel";
import { defaultModelCapabilityConfig } from "../src/lib/model-capabilities";
import { defaultConfig, useConfigStore, type AiConfig } from "../src/stores/use-config-store";
import { useUserStore } from "../src/stores/use-user-store";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";

const noop = () => {};
// 面板会挂载带 useInfiniteQuery 的子组件（图片风格选择器），SSR 渲染必须提供 QueryClient。
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
const model = "panel-test::video-test";
const input = { textCount: 1, imageCount: 0, videoCount: 0, audioCount: 0, characterCount: 0 };
const restore: Array<() => void> = [];
afterEach(() => { restore.splice(0).reverse().forEach((reset) => reset()); });

function configFixture(declared: boolean): AiConfig {
    const capabilityConfig = defaultModelCapabilityConfig();
    const video = capabilityConfig.video!;
    video.operations = ["text_to_video"];
    video.ratios = ["16:9"];
    video.defaultRatio = "16:9";
    video.duration = { selection: "enum", values: [6], default: 6 };
    video.resolutions = ["720p"];
    video.defaultResolution = "720p";
    return {
        ...defaultConfig, model, videoModel: model, models: [model], videoModels: [model],
        channels: [{
            id: "panel-test", name: "Panel test", baseUrl: "/api", apiKey: "system", scope: "system", apiFormat: "openai", models: ["video-test"],
            modelCosts: [{ model: "video-test", capability: "video", billingMode: "per_second", unitPriceMicrocredits: 1, ...(declared ? { capabilityConfig } : {}) }],
        }],
    };
}

function nodeFixture(type: CanvasNodeType, seconds = "6"): CanvasNodeData {
    return { id: "panel-node", type, title: "Video", position: { x: 0, y: 0 }, width: 400, height: 240, metadata: { generationMode: "video", model, seconds, composerContent: "镜头推进" } };
}

function renderPanel(kind: "prompt" | "config", declared: boolean, seconds: string, simple = false) {
    const config = configFixture(declared);
    // SSR reads Zustand's initial snapshot, not its live getState snapshot.
    const initialConfig = useConfigStore.getInitialState();
    const originalConfig = initialConfig.config;
    const user = useUserStore.getInitialState();
    const originalFeatures = user.features;
    initialConfig.config = config;
    user.features = { ...originalFeatures, customChannelsEnabled: true, creditsEnabled: false };
    restore.push(() => { initialConfig.config = originalConfig; }, () => { user.features = originalFeatures; });
    const node = nodeFixture(kind === "prompt" ? CanvasNodeType.Video : CanvasNodeType.Config, seconds);
    const common = { node, isRunning: false, onConfigChange: noop, onGenerate: noop, workspaceMode: simple ? "simple" as const : "professional" as const };
    const markup = renderToStaticMarkup(
        <QueryClientProvider client={queryClient}>
            {kind === "prompt"
                ? <CanvasNodePromptPanel {...common} projectId="test-project" onPromptChange={noop} />
                : <CanvasConfigNodePanel {...common} inputSummary={input} onComposerToggle={noop} />}
        </QueryClientProvider>,
    );
    const buttons = markup.match(/<button\b[^>]*>[\s\S]*?<\/button>/g) || [];
    const submit = buttons.find((button) => kind === "prompt" ? button.includes("canvas-node-composer-submit ") : button.includes("开始生成"));
    expect(submit).toBeDefined();
    const status = /class="model-capability-hint" data-state="([^"]+)"/.exec(markup)?.[1];
    return { markup, submit: submit!, node, status };
}

describe("real canvas video panels expose admission and block generation", () => {
    for (const kind of ["prompt", "config"] as const) {
        test(`${kind} panel keeps declared valid video generation available`, () => {
            const { status, submit } = renderPanel(kind, true, "6");
            expect(status).toBe("ready");
            expect(submit).not.toContain('disabled=""');
        });

        for (const simple of [false, true]) {
            test(`${kind} panel warns and disables an already-selected unknown model in ${simple ? "simple" : "professional"} mode`, () => {
                const { markup, submit, status } = renderPanel(kind, false, "6", simple);
                expect(status).toBe("unknown");
                expect(markup).toContain("能力待确认");
                expect(submit).toContain('disabled=""');
                expect(markup).toContain("清除规格约束，重新选模型");
            });
        }

        test(`${kind} panel preserves invalid explicit duration and offers a reset instead of generating`, () => {
            const { markup, submit, node, status } = renderPanel(kind, true, "99");
            expect(status).toBe("incompatible");
            expect(markup).toContain("99 秒");
            expect(submit).toContain('disabled=""');
            expect(node.metadata?.seconds).toBe("99");
            expect(markup).toContain("清除规格约束，重新选模型");
        });
    }

    test("a removed saved video model is not replaced by a different global model", () => {
        const config = configFixture(true);
        const node = nodeFixture(CanvasNodeType.Video);
        node.metadata!.model = "removed-channel::saved-video";
        const request = buildNodeConfig(config, node, "video", { capability: "video", input });
        expect(request.model).toBe("removed-channel::saved-video");
    });
});
