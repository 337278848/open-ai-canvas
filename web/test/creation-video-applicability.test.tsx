import { describe, expect, test } from "bun:test";
import { createRef, type ComponentProps } from "react";
import { renderToStaticMarkup } from "react-dom/server";

import { assessModelApplicability } from "../src/lib/model-applicability";
import { defaultModelCapabilityConfig, type VideoCapabilityConfig } from "../src/lib/model-capabilities";
import { canvasResourceMentionToken } from "../src/lib/canvas/canvas-resource-references";
import { defaultConfig, type AiConfig } from "../src/stores/use-config-store";
import { buildCreationVideoRequirements, creationVideoSelectionRequirements, resolveCreationVideoSettings, type CreationVideoSettings } from "../src/pages/create";
import { CreationComposer } from "../src/pages/create/creation-workspace";
import { buildCreationMentionReferences, expandCreationPrompt, reconcileCreationAttachmentLimit, selectedCreationReferences } from "../src/pages/create/creation-references";
import { splitCreationAttachments, type CreationAttachment } from "../src/pages/create/creation-assets";

const model = "test::video-model";

function videoProfile(overrides: Partial<VideoCapabilityConfig> = {}): VideoCapabilityConfig {
    const base = defaultModelCapabilityConfig("newapi-channel-2", "seedance-1.0-pro").video!;
    return {
        ...base,
        references: { ...base.references, minImages: 0, maxImages: 1, maxVideos: 1, maxAudios: 1, maxImageBytes: 1024, maxVideoBytes: 4096, maxAudioBytes: 2048, maxVideoDurationSeconds: 10, maxAudioDurationSeconds: 10 },
        duration: { selection: "enum", values: [4, 6], default: 6 },
        ratios: ["16:9", "9:16"],
        defaultRatio: "16:9",
        resolutions: ["720p"],
        defaultResolution: "720p",
        operations: ["text_to_video", "image_to_video", "reference_to_video", "audio_to_video"],
        generateAudio: { supported: false, default: false },
        watermark: { supported: false, default: false },
        ...overrides,
    };
}

function videoConfig(profile = videoProfile()): AiConfig {
    return {
        ...defaultConfig,
        videoModel: model,
        models: [model],
        videoModels: [model],
        videoGenerateAudio: "true",
        videoWatermark: "true",
        channels: [{
            id: "test",
            name: "Test",
            scope: "user",
            apiKey: "test-key",
            baseUrl: "https://example.test",
            apiFormat: "openai",
            interfaceType: "newapi-channel-2",
            models: ["video-model"],
            modelCosts: [{
                model: "video-model",
                capability: "video",
                protocol: "newapi-channel-2",
                billingMode: "per_second",
                unitPriceMicrocredits: 1,
                capabilityConfig: { ...defaultModelCapabilityConfig("newapi-channel-2", "video-model"), video: profile },
            }],
        }],
    };
}

function media(): CreationAttachment[] {
    return [
        { id: "image", name: "image.png", type: "image/png", dataUrl: "/image.png", previewUrl: "/image.png", bytes: 100 },
        { id: "video", name: "video.mp4", type: "video/mp4", url: "/video.mp4", previewUrl: "/video.mp4", bytes: 200, durationMs: 6500 },
        { id: "audio", name: "audio.mp3", type: "audio/mpeg", url: "/audio.mp3", previewUrl: "", bytes: 300, durationMs: 3000 },
    ];
}

function draft(attachments = media()) {
    const references = buildCreationMentionReferences([], attachments);
    const prompt = `保留这些素材 ${references.map(canvasResourceMentionToken).join(" ")}`;
    return { attachments, references, prompt };
}

function composerProps(profile = videoProfile(), overrides: Partial<ComponentProps<typeof CreationComposer>> = {}): ComponentProps<typeof CreationComposer> {
    const config = videoConfig(profile);
    const state = draft();
    const settings = resolveCreationVideoSettings(profile, {});
    const requirements = buildCreationVideoRequirements(state.prompt, state.attachments, settings);
    const noop = () => {};
    return {
        variant: "empty",
        mode: "video",
        ...state,
        setPrompt: noop,
        busy: false,
        generationActive: false,
        referenceReplacementBusy: false,
        maxReferences: Number.POSITIVE_INFINITY,
        onRemoveAttachment: noop,
        onClearAttachments: noop,
        onClearComposer: noop,
        onReorderAttachments: noop,
        onReplaceAttachment: noop,
        onReplaceReferenceFiles: noop,
        onOpenLibrary: noop,
        onModeChange: noop,
        model,
        modelRequirements: requirements,
        selectionRequirements: creationVideoSelectionRequirements(requirements, {}),
        onResetVideoSettings: noop,
        videoProfile: profile,
        imageProfile: defaultModelCapabilityConfig().image!,
        config,
        onModelChange: noop,
        ...settings,
        setRatio: noop,
        setSeconds: noop,
        quality: "auto",
        setQuality: noop,
        setVideoQuality: noop,
        setVideoGenerateAudio: noop,
        setVideoWatermark: noop,
        count: "1",
        setCount: noop,
        textStreaming: true,
        setTextStreaming: noop,
        textThinking: false,
        setTextThinking: noop,
        promptOptimizerProvider: null,
        composerFocusRef: createRef<HTMLTextAreaElement>(),
        onPromptFocus: noop,
        onSubmit: noop,
        ...overrides,
    };
}

function renderedButton(markup: string, marker: string) {
    const button = (markup.match(/<button\b[^>]*>/g) || []).find((tag) => tag.includes(marker));
    expect(button).toBeDefined();
    return button!;
}

describe("home video requirements", () => {
    test("mixed media use independent counts and preserve every attachment and mention", () => {
        const state = draft();
        const before = structuredClone(state);
        const retained = reconcileCreationAttachmentLimit(state.attachments, state.references, Number.POSITIVE_INFINITY);
        const requirements = buildCreationVideoRequirements(
            expandCreationPrompt(state.prompt, selectedCreationReferences(state.prompt, state.references), retained.attachments),
            retained.attachments,
            resolveCreationVideoSettings(videoProfile(), {}),
        );

        expect(retained.attachments).toBe(state.attachments);
        expect(retained.removedReferences).toEqual([]);
        expect(requirements.input).toEqual({ textCount: 1, imageCount: 1, videoCount: 1, audioCount: 1, characterCount: 0 });
        expect(requirements.media).toEqual([
            { kind: "image", bytes: 100, durationSeconds: undefined },
            { kind: "video", bytes: 200, durationSeconds: 6.5 },
            { kind: "audio", bytes: 300, durationSeconds: 3 },
        ]);
        expect(assessModelApplicability(videoConfig(), model, requirements).status).toBe("ready");
        expect(state).toEqual(before);
        const payload = splitCreationAttachments(retained.attachments);
        expect(payload.referenceImages).toEqual([state.attachments[0]]);
        expect(payload.referenceVideos).toEqual([state.attachments[1]]);
        expect(payload.referenceAudios).toEqual([state.attachments[2]]);
    });

    test("video and audio maxima block submission instead of deleting inputs", () => {
        for (const source of [media()[1], media()[2]]) {
            const state = draft([...media(), { ...source, id: "extra" }]);
            const snapshot = structuredClone(state);
            const requirements = buildCreationVideoRequirements(state.prompt, state.attachments, resolveCreationVideoSettings(videoProfile(), {}));
            const result = assessModelApplicability(videoConfig(), model, requirements);
            expect(result.status).toBe("incompatible");
            expect(result.reason).not.toBe("");
            expect(state).toEqual(snapshot);
        }
    });

    test("known file limits and durations participate in assessment, missing metadata stays unknown", () => {
        const settings = resolveCreationVideoSettings(videoProfile(), {});
        const large = { ...media()[1], bytes: 4097 };
        const long = { ...media()[1], durationMs: 10001 };
        for (const attachment of [large, long]) {
            const requirements = buildCreationVideoRequirements("镜头", [attachment], settings);
            expect(assessModelApplicability(videoConfig(), model, requirements).status).toBe("incompatible");
        }
        const unknown: CreationAttachment = { id: "remote", name: "remote.mp4", type: "video/mp4", url: "/remote.mp4", previewUrl: "" };
        expect(buildCreationVideoRequirements("镜头", [unknown], settings).media).toEqual([{ kind: "video", bytes: undefined, durationSeconds: undefined }]);
    });

    test("the actual expanded prompt is assessed without truncation", () => {
        const profile = videoProfile();
        profile.references.promptMaxChars = 3;
        const prompt = "镜头包含额外技能上下文";
        const requirements = buildCreationVideoRequirements(prompt, [], resolveCreationVideoSettings(profile, {}));
        expect(requirements.prompt).toBe(prompt);
        expect(assessModelApplicability(videoConfig(profile), model, requirements).status).toBe("incompatible");
    });
});

describe("home video explicit settings", () => {
    test("old preferences do not become hard selection requirements", () => {
        const settings = resolveCreationVideoSettings(videoProfile(), {}, { ratio: "9:16", seconds: "4", videoQuality: "720" });
        const requirements = buildCreationVideoRequirements("镜头", [], settings);
        const selection = creationVideoSelectionRequirements(requirements, {});
        expect(requirements.options).toMatchObject({ size: "9:16", videoSeconds: 4, vquality: "720" });
        expect(selection.options).toEqual({});
        expect(selection.videoSeconds).toBeUndefined();
        expect(selection.input).toBe(requirements.input);
        expect(selection.media).toBe(requirements.media);
        expect(selection.prompt).toBe(requirements.prompt);
    });

    test("explicit duration, ratio, resolution and booleans survive incompatible model changes", () => {
        const explicit: CreationVideoSettings = { ratio: "21:9", seconds: "20", videoQuality: "1080", videoGenerateAudio: true, videoWatermark: true };
        const settings = resolveCreationVideoSettings(videoProfile(), explicit);
        expect(settings).toEqual(explicit);
        const requirements = buildCreationVideoRequirements("镜头", [], settings);
        expect(creationVideoSelectionRequirements(requirements, explicit)).toEqual(requirements);
        expect(assessModelApplicability(videoConfig(), model, requirements).status).toBe("incompatible");
    });

    test("only unset fields follow the new model defaults", () => {
        const profile = videoProfile({ defaultRatio: "9:16", duration: { selection: "enum", values: [8], default: 8 }, resolutions: ["768p"], defaultResolution: "768p" });
        const explicit = { seconds: "6" };
        const settings = resolveCreationVideoSettings(profile, explicit);
        expect(settings).toMatchObject({ seconds: "6", ratio: "9:16", videoQuality: "768" });
        expect(creationVideoSelectionRequirements(buildCreationVideoRequirements("镜头", [], settings), explicit).options).toEqual({ videoSeconds: 6 });
    });

    test("legacy true flags are ignored, while explicit false survives a model default of true", () => {
        const defaults = resolveCreationVideoSettings(videoProfile(), {}, { videoGenerateAudio: true, videoWatermark: true });
        expect(defaults.videoGenerateAudio).toBe(false);
        expect(defaults.videoWatermark).toBe(false);
        const profile = videoProfile({ generateAudio: { supported: true, default: true }, watermark: { supported: true, default: true } });
        expect(resolveCreationVideoSettings(profile, {})).toMatchObject({ videoGenerateAudio: true, videoWatermark: true });
        const explicit = { videoGenerateAudio: false, videoWatermark: false };
        const settings = resolveCreationVideoSettings(profile, explicit);
        const selection = creationVideoSelectionRequirements(buildCreationVideoRequirements("镜头", [], settings), explicit);
        expect(settings).toMatchObject(explicit);
        expect(selection.options).toEqual(explicit);
    });

    test("resetting explicit requirements unblocks a 768p model without losing prompt or media", () => {
        const state = draft();
        const snapshot = structuredClone(state);
        const currentProfile = videoProfile();
        const targetProfile = videoProfile({ resolutions: ["768p"], defaultResolution: "768p" });
        const explicit = { videoQuality: "720" };
        const requirements = buildCreationVideoRequirements(state.prompt, state.attachments, resolveCreationVideoSettings(currentProfile, explicit));
        expect(assessModelApplicability(videoConfig(targetProfile), model, creationVideoSelectionRequirements(requirements, explicit)).status).toBe("incompatible");

        const resetSettings = resolveCreationVideoSettings(currentProfile, {});
        const resetRequirements = buildCreationVideoRequirements(state.prompt, state.attachments, resetSettings);
        const targetSelection = creationVideoSelectionRequirements(resetRequirements, {});
        expect(resetSettings.videoQuality).toBe("720");
        expect(assessModelApplicability(videoConfig(targetProfile), model, targetSelection).status).toBe("ready");
        expect(resolveCreationVideoSettings(targetProfile, {}).videoQuality).toBe("768");
        expect(state).toEqual(snapshot);
    });

    test("blank and out-of-range explicit duration are not normalized on blur or model change", () => {
        for (const seconds of ["", "2.5", "100"]) {
            const settings = resolveCreationVideoSettings(videoProfile(), { seconds });
            expect(settings.seconds).toBe(seconds);
            const requirements = buildCreationVideoRequirements("镜头", [], settings);
            expect(assessModelApplicability(videoConfig(), model, requirements).status).toBe("incompatible");
        }
    });
});

describe("home video composer rendering", () => {
    test("text-to-video-only models still allow adding references before choosing another model", () => {
        const profile = videoProfile({ operations: ["text_to_video"] });
        profile.references.maxImages = 0;
        const props = composerProps(profile, { attachments: [], references: [], maxReferences: 0 });
        const markup = renderToStaticMarkup(<CreationComposer {...props} />);
        expect(renderedButton(markup, "creation-reference-add-button")).not.toContain("disabled");
        expect(markup).toContain("按模型默认规格");
    });

    test("missing required images blocks send but leaves the library accessible", () => {
        const profile = videoProfile({ operations: ["image_to_video"] });
        profile.references.minImages = 1;
        const requirements = buildCreationVideoRequirements("镜头", [], resolveCreationVideoSettings(profile, {}));
        const result = assessModelApplicability(videoConfig(profile), model, requirements);
        expect(result.status).toBe("needs_input");
        const props = composerProps(profile, { attachments: [], references: [], modelRequirements: requirements, generationBlockedReason: result.reason });
        const markup = renderToStaticMarkup(<CreationComposer {...props} />);
        expect(renderedButton(markup, "creation-submit")).toContain("disabled");
        expect(renderedButton(markup, "creation-reference-add-button")).not.toContain("disabled");
        expect(markup).toContain('role="alert"');
    });

    test("incompatible settings remain visible and disable generation without hiding attachments", () => {
        const explicit = { seconds: "20", videoQuality: "1080" };
        const props = composerProps(videoProfile(), { ...explicit, generationBlockedReason: "不支持当前视频时长" });
        props.modelRequirements = buildCreationVideoRequirements(props.prompt, props.attachments, { ...resolveCreationVideoSettings(props.videoProfile, {}), ...explicit });
        const markup = renderToStaticMarkup(<CreationComposer {...props} />);
        expect(renderedButton(markup, "creation-submit")).toContain("disabled");
        expect(markup).toContain("视频时长：20秒");
        expect(markup).toContain("1080P");
        for (const attachment of props.attachments) expect(markup).toContain(`移除 ${attachment.name}`);
    });

    test("image and text modes do not gain video constraints or a video reset action", () => {
        for (const mode of ["image", "text"] as const) {
            const props = composerProps(videoProfile(), { mode, attachments: [], references: [], modelRequirements: { capability: mode }, maxReferences: 6 });
            const markup = renderToStaticMarkup(<CreationComposer {...props} />);
            expect(renderedButton(markup, "creation-submit")).not.toContain("disabled");
            expect(markup).not.toContain("按模型默认规格");
            expect(markup).not.toContain('aria-label="当前模型能力"');
        }
    });

    test("a valid mixed-media draft can submit despite having more items than maxImages", () => {
        const props = composerProps();
        expect(props.attachments.length).toBeGreaterThan(props.videoProfile.references.maxImages);
        expect(assessModelApplicability(props.config, props.model, props.modelRequirements).status).toBe("ready");
        expect(renderedButton(renderToStaticMarkup(<CreationComposer {...props} />), "creation-submit")).not.toContain("disabled");
    });

    test("undeclared capabilities block generation without removing the library or reset action", () => {
        const props = composerProps();
        props.config.channels[0].modelCosts![0].capabilityConfig = undefined;
        const result = assessModelApplicability(props.config, props.model, props.modelRequirements);
        expect(result.status).toBe("unknown");
        props.generationBlockedReason = result.reason;
        const markup = renderToStaticMarkup(<CreationComposer {...props} />);
        expect(renderedButton(markup, "creation-submit")).toContain("disabled");
        expect(renderedButton(markup, "creation-reference-add-button")).not.toContain("disabled");
        expect(markup).toContain("按模型默认规格");
    });
});
