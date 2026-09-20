import { defaultImageCapabilityConfig, modelCapabilityConfigFor, normalizeImageValue, normalizeVideoValue, STANDARD_IMAGE_SIZE_VALUES, videoDurationAllowed, videoResolutionRequest, type ImageCapabilityConfig, type VideoCapabilityConfig } from "@/lib/model-capabilities";
import { videoResolutionComparisonKey } from "@/lib/video-generation-options";
import { imageSizePresets } from "@/lib/image-size-presets";
import { modelOptionName, resolveModelChannel, selectableModelsByCapability, type AiConfig, type ModelCapability } from "@/stores/use-config-store";

export type ModelInputSummary = {
    textCount: number;
    imageCount: number;
    videoCount: number;
    audioCount: number;
    characterCount: number;
};

export type ModelRequirements = {
    capability?: ModelCapability;
    input?: ModelInputSummary;
    videoOperation?: string;
    /** True for a chosen mode; absent for legacy node metadata that follows connected inputs. */
    videoOperationExplicit?: boolean;
    videoSeconds?: string;
    imageSize?: string;
    options?: Record<string, unknown>;
    /** Known metadata only; absence must not be interpreted as a zero-byte file. */
    media?: Array<{ kind: "image" | "video" | "audio"; bytes?: number; durationSeconds?: number }>;
    prompt?: string;
};

export type DisplayModelGroup = {
    key: string;
    label: string;
    models: string[];
};

export type ModelReferenceLimits = {
    maxImages: number;
    maxVideos: number;
    maxAudios: number;
};

export function groupModelsByDisplayName(config: AiConfig, models: string[]): DisplayModelGroup[] {
    const groups = new Map<string, DisplayModelGroup>();
    models.forEach((model) => {
        const channel = resolveModelChannel(config, model);
        const label = configuredModelDisplayName(config, model);
        const key = `${channel.id}\u0000${label.toLocaleLowerCase()}`;
        const current = groups.get(key);
        if (current) current.models.push(model);
        else groups.set(key, { key, label, models: [model] });
    });
    return Array.from(groups.values());
}

export function configuredModelDisplayName(config: AiConfig, value: string) {
    const model = modelOptionName(value);
    const channel = resolveModelChannel(config, value);
    return channel.modelCosts?.find((item) => item.model === model)?.displayName?.trim() || model;
}

export function modelCompatibilityError(config: AiConfig, model: string, requirements?: ModelRequirements) {
    const capability = requirements?.capability;
    if (!capability) return "";
    const input = requirements?.input;
    const visualInputCount = input ? input.imageCount + input.characterCount : 0;
    const channel = resolveModelChannel(config, model);
    const logicalCost = channel.modelCosts?.find((item) => item.model === modelOptionName(model));
    const logicalSpecs = logicalCost?.logicalCapabilityProfiles?.length ? logicalCost.logicalCapabilityProfiles : logicalCost?.logicalCapabilitySpec ? [logicalCost.logicalCapabilitySpec] : [];
    if (logicalSpecs.length) {
        const publicOptionNames = logicalCost?.logicalCapabilitySpec?.options || Object.assign({}, ...logicalSpecs.map((spec) => spec.options || {}));
        const logicalRequirements = {
            ...requirements,
            options: Object.fromEntries(Object.entries(requirements.options || {}).filter(([name]) => Boolean(publicOptionNames[name]))),
        };
        const errors = logicalSpecs.map((spec) => logicalModelCompatibilityError(spec, logicalRequirements, visualInputCount));
        return errors.some((error) => !error) ? "" : errors[0] || "当前输入不受支持";
    }

    if (capability === "image") {
        const image = modelCapabilityConfigFor(config, model).image!;
        // 尺寸兼容不依赖输入摘要：无输入时（如画布重试/工具链）也要按尺寸过滤组内模型。
        if (requirements.imageSize && !image.size.allowCustom && !image.size.values.includes(requirements.imageSize)) return "不支持当前尺寸";
        if (!input) return "";
        if (input.videoCount > 0) return "图片模型不支持参考视频";
        if (input.audioCount > 0) return "图片模型不支持参考音频";
        if (visualInputCount > image.references.maxImages) return `最多支持 ${image.references.maxImages} 张参考图`;
        return "";
    }

    if (capability === "video") {
        const profile = modelCapabilityConfigFor(config, model).video!;
        const seconds = requirements.videoSeconds ?? requirements.options?.videoSeconds;
        if (seconds !== undefined && seconds !== "" && !videoDurationAllowed(profile, Number(seconds))) return "不支持当前视频时长";
        const ratio = String(requirements.options?.size ?? requirements.options?.aspectRatio ?? "").trim();
        if (ratio && !videoRatioMatches(profile.ratios, ratio)) return "不支持当前画面比例";
        const resolution = String(requirements.options?.vquality ?? requirements.options?.resolution ?? "").trim();
        if (resolution && !["auto", "default", "medium", "high"].includes(resolution.toLowerCase()) && profile.resolutions.length && !videoResolutionRequest(profile, resolution)) return "不支持当前分辨率";
        if (isTrueOption(requirements.options?.videoGenerateAudio) && !profile.generateAudio.supported) return "不支持开启生成声音";
        if (isTrueOption(requirements.options?.videoWatermark) && !profile.watermark.supported) return "不支持开启水印";
        const mediaError = videoReferenceMetadataError(profile, requirements.media);
        if (mediaError) return mediaError;
        if (requirements.prompt) {
            const promptError = modelPromptLengthError(config, model, "video", requirements.prompt);
            if (promptError) return promptError;
        }
        if (!input) return "";
        if (visualInputCount > profile.references.maxImages) return `最多支持 ${profile.references.maxImages} 张参考图`;
        if (input.videoCount > profile.references.maxVideos) return `最多支持 ${profile.references.maxVideos} 个参考视频`;
        if (input.audioCount > profile.references.maxAudios) return `最多支持 ${profile.references.maxAudios} 个参考音频`;
        if (visualInputCount < profile.references.minImages) return `至少需要 ${profile.references.minImages} 张参考图，当前 ${visualInputCount} 张`;
        const operation = resolvedVideoRequirementOperation(requirements)!;
        if (operation !== "concat" && !profile.operations.includes(operation)) return `不支持${videoOperationLabel(operation)}`;
        return "";
    }

    if (!input) return "";

    if (capability === "text") {
        return input.audioCount > 0 ? "文本模型不支持参考音频" : "";
    }

    if (input.characterCount > 1) return "角色配音一次只能引用一个角色卡";
    return input.imageCount > 0 || input.videoCount > 0 || input.audioCount > 0 ? "音频模型只接受文本或单个角色卡输入" : "";
}

function isTrueOption(value: unknown) {
    return value === true || value === "true";
}

// Match the backend's pixel-size compatibility for legacy saved canvas nodes.
function videoRatioMatches(options: string[], value: string) {
    const normalized = value.toLowerCase().replaceAll("×", "x");
    if (options.some((option) => option.trim().toLowerCase() === normalized)) return true;
    const dimensions = /^(\d+(?:\.\d+)?)x(\d+(?:\.\d+)?)$/.exec(normalized);
    if (!dimensions || Number(dimensions[1]) <= 0 || Number(dimensions[2]) <= 0) return false;
    const actual = Number(dimensions[1]) / Number(dimensions[2]);
    return options.some((option) => {
        const parts = option.split(":").map(Number);
        const ratio = parts.length === 2 && parts.every((part) => Number.isFinite(part) && part > 0) ? parts[0] / parts[1] : 0;
        return ratio > 0 && Math.abs(actual - ratio) / ratio < 0.01;
    });
}

function videoReferenceMetadataError(profile: VideoCapabilityConfig, media: ModelRequirements["media"]) {
    for (const item of media || []) {
        const { maxImageBytes, maxVideoBytes, maxAudioBytes, maxVideoDurationSeconds, maxAudioDurationSeconds } = profile.references;
        const label = item.kind === "image" ? "参考图片" : item.kind === "video" ? "参考视频" : "参考音频";
        const maxBytes = item.kind === "image" ? maxImageBytes : item.kind === "video" ? maxVideoBytes : maxAudioBytes;
        if (maxBytes > 0 && item.bytes !== undefined && item.bytes > maxBytes) return `${label}单个文件不能超过 ${Number((maxBytes / 1024 / 1024).toFixed(2))} MB`;
        const maxSeconds = item.kind === "video" ? maxVideoDurationSeconds : item.kind === "audio" ? maxAudioDurationSeconds : 0;
        if (maxSeconds > 0 && item.durationSeconds !== undefined && item.durationSeconds > maxSeconds) return `${label}单个时长不能超过 ${maxSeconds} 秒`;
    }
    return "";
}

export function modelPromptLengthError(config: AiConfig, model: string, capability: ModelCapability, prompt: string) {
    const channel = resolveModelChannel(config, model);
    const configuredProfile = channel.modelCosts?.find((item) => item.model === modelOptionName(model))?.capabilityConfig;
    if (!configuredProfile) return "";
    const profile = modelCapabilityConfigFor(config, model);
    const maxChars = capability === "text" ? profile.text?.references.promptMaxChars : capability === "image" ? profile.image?.references.promptMaxChars : capability === "video" ? profile.video?.references.promptMaxChars : undefined;
    if (!maxChars || maxChars <= 0) return "";
    const actualChars = Array.from(prompt).length;
    if (actualChars <= maxChars) return "";
    const label = capability === "text" ? "文本" : capability === "image" ? "图片" : "视频";
    return `当前${label}模型提示词最多 ${maxChars} 个字符，完整提示词为 ${actualChars} 个字符。系统不会自动截断，请精简当前输入、连线内容或技能上下文后重试`;
}

export function modelRequestOptions(config: AiConfig, capability: ModelCapability) {
    switch (capability) {
        case "image":
            return { size: config.size, quality: config.quality, transparentBackground: config.transparentBackground === "true", count: Number(config.count) };
        case "video":
            return { size: config.size, videoSeconds: Number(config.videoSeconds), vquality: config.vquality, videoGenerateAudio: config.videoGenerateAudio === "true", videoWatermark: config.videoWatermark === "true" };
        case "audio":
            return { audioVoice: config.audioVoice, audioFormat: config.audioFormat, audioSpeed: Number(config.audioSpeed) };
        default:
            return {};
    }
}

function logicalModelCompatibilityError(spec: NonNullable<NonNullable<AiConfig["channels"][number]["modelCosts"]>[number]["logicalCapabilitySpec"]>, requirements: ModelRequirements, visualInputCount: number) {
    if (requirements.capability && spec.capability !== requirements.capability) return "不支持当前生成类型";
    const input = requirements.input;
    const counts: Record<string, number> = {
        text: 0,
        image: visualInputCount,
        video: input?.videoCount || 0,
        audio: input?.audioCount || 0,
    };
    for (const [kind, count] of Object.entries(counts)) {
        const constraint = spec.inputs?.[kind];
        if (!constraint && count > 0) return `不支持${kind}输入`;
        if (constraint && (count < constraint.min || count > constraint.max)) return `${kind}输入需为 ${constraint.min}-${constraint.max} 个`;
    }
    const operation = requirements.capability === "video" ? resolvedVideoRequirementOperation(requirements) : requirements.videoOperation;
    if (operation && spec.operations?.length && !spec.operations.includes(operation)) return "不支持当前生成模式";
    // 图片创作状态也会携带全局默认视频时长；这个字段只对视频模型有意义，
    // 不能把它拼进图片逻辑模型的能力匹配，否则图片模型会被误判为“不支持当前时长”。
    const options = {
        ...requirements.options,
        ...(requirements.capability === "video" && requirements.videoSeconds ? { videoSeconds: requirements.videoSeconds } : {}),
        ...(requirements.capability === "image" && requirements.imageSize ? { size: requirements.imageSize } : {}),
    };
    for (const [name, value] of Object.entries(options)) {
        if (value === undefined || value === null || value === "") continue;
        const constraint = spec.options?.[name];
        if (!constraint || !logicalOptionMatches(name, constraint, value)) return logicalOptionError(name);
    }
    return "";
}

function logicalOptionMatches(name: string, constraint: { values?: unknown[]; min?: number; max?: number; step?: number }, value: unknown) {
    if (constraint.values?.length) {
        const requested = normalizeLogicalOptionValue(name, value);
        return constraint.values.some((candidate) => normalizeLogicalOptionValue(name, candidate) === requested);
    }
    const numeric = Number(value);
    if (!Number.isFinite(numeric)) return false;
    if (constraint.min !== undefined && numeric < constraint.min) return false;
    if (constraint.max !== undefined && numeric > constraint.max) return false;
    if (constraint.step !== undefined && constraint.min !== undefined) return Math.abs((numeric - constraint.min) / constraint.step - Math.round((numeric - constraint.min) / constraint.step)) < 1e-9;
    return true;
}

function normalizeLogicalOptionValue(name: string, value: unknown) {
    const normalized = String(value).trim().toLowerCase();
    if (name !== "vquality" && name !== "resolution") return normalized;
    return videoResolutionComparisonKey(normalized);
}

function logicalOptionError(name: string) {
    const label: Record<string, string> = {
        size: "尺寸",
        quality: "质量",
        transparentBackground: "透明背景",
        count: "输出数量",
        videoSeconds: "时长",
        vquality: "分辨率",
        videoGenerateAudio: "同步音频",
        videoWatermark: "水印设置",
        audioVoice: "音色",
        audioFormat: "音频格式",
        audioSpeed: "语速",
    };
    return `不支持当前${label[name] || name}`;
}

export function compatibleModelInGroup(config: AiConfig, models: string[], requirements?: ModelRequirements, preferred?: string) {
    const compatible = models.filter((model) => !modelCompatibilityError(config, model, requirements));
    if (!compatible.length) return "";
    if (compatible.length === 1) return compatible[0];
    const priceOf = (model: string) => {
        const channel = resolveModelChannel(config, model);
        const cost = channel.modelCosts?.find((item) => item.model === modelOptionName(model));
        return cost && Number.isFinite(cost.unitPriceMicrocredits) ? cost.unitPriceMicrocredits : Number.POSITIVE_INFINITY;
    };
    return compatible.sort((left, right) => priceOf(left) - priceOf(right) || (left === preferred ? -1 : right === preferred ? 1 : 0))[0];
}

export function resolveCompatibleModel(config: AiConfig, selected: string, requirements?: ModelRequirements) {
    if (!requirements?.capability) return selected;
    const options = selectableModelsByCapability(config, requirements.capability);
    if (!options.length) return selected;
    const selectedGroup = groupModelsByDisplayName(config, options).find((group) => group.models.includes(selected));
    if (!selectedGroup) return selected;
    return compatibleModelInGroup(config, selectedGroup.models, requirements, selected);
}

// 同显示名分组的模型族：尺寸/比例/分辨率选项取组内全部模型配置的并集，
// 让用户能看到并选择任意成员支持的能力，选中后由兼容路由落到具体模型。
// 质量、透明背景等其余能力取当前选中（路由后）模型的配置，与创作页面一致。
export function mergedImageCapabilityConfig(config: AiConfig, selected: string): ImageCapabilityConfig {
    const options = selectableModelsByCapability(config, "image");
    const group = options.length ? groupModelsByDisplayName(config, options).find((item) => item.models.includes(selected)) : undefined;
    const models = group?.models.length ? group.models : [selected];
    const selectedProfile = modelCapabilityConfigFor(config, selected).image;
    const profiles = models.map((model) => modelCapabilityConfigFor(config, model).image).filter((profile): profile is ImageCapabilityConfig => Boolean(profile));
    if (profiles.length <= 1) return selectedProfile || defaultImageCapabilityConfig();
    const concreteValues = Array.from(new Set(profiles.flatMap((profile) => profile.size.values).filter((value) => value !== "*")));
    const allowCustom = profiles.some((profile) => profile.size.allowCustom || profile.size.values.includes("*"));
    const values = concreteValues.length ? concreteValues : allowCustom ? [...STANDARD_IMAGE_SIZE_VALUES] : [];
    const base = selectedProfile || profiles[0];
    const presets = profiles.some((profile) => profile.size.presets) ? [...new Map(profiles.flatMap((profile) => imageSizePresets(profile)).map((preset) => [`${preset.tier}:${preset.ratio}:${preset.size}`, preset])).values()] : undefined;
    return { ...base, size: { ...base.size, values, allowCustom, presets } };
}

// 切换模型后初始化图片参数为该模型能力默认值，避免旧参数在目标模型族不兼容导致无法切换。
export function defaultImageParamsForModel(config: AiConfig, model: string): Pick<AiConfig, "size" | "quality" | "transparentBackground"> {
    const image = modelCapabilityConfigFor(config, model).image;
    if (!image) return { size: "1:1", quality: "auto", transparentBackground: "false" };
    const sizeValues = image.size.values.filter((value) => value !== "*");
    const sizeDefault = image.size.default !== "*" && image.size.default ? image.size.default : sizeValues[0] || "1:1";
    return {
        size: sizeDefault,
        quality: image.quality.default || "auto",
        transparentBackground: String(image.transparentBackground.default ?? false),
    };
}


export type ModelGenerationDefaults = Pick<AiConfig, "size" | "quality" | "transparentBackground" | "count" | "videoSeconds" | "vquality" | "videoGenerateAudio" | "videoWatermark">;

type ModelVideoBooleanOptions = Pick<AiConfig, "videoGenerateAudio" | "videoWatermark">;

export function resolveModelGenerationDefaults(
    config: AiConfig,
    model: string,
    capability: "image" | "video" | undefined,
    explicit: Partial<ModelGenerationDefaults> = {},
    fallback: Partial<ModelGenerationDefaults> = {},
): Partial<ModelGenerationDefaults> {
    if (!capability) return {};
    const channel = resolveModelChannel(config, model);
    const cost = channel.modelCosts?.find((item) => item.model === modelOptionName(model));
    const isManagedModel = Boolean(cost?.logicalModelId || cost?.logicalCapabilitySpec);
    // A channel model capability profile is an explicit per-model contract too.
    // The persisted global values are legacy defaults and must not override a
    // model's configured duration, ratio, or resolution on a new canvas node.
    const hasModelCapabilityProfile = Boolean(cost?.capabilityConfig);
    const source = (key: keyof ModelGenerationDefaults) => explicit[key] ?? (isManagedModel || hasModelCapabilityProfile ? undefined : fallback[key]);
    const profile = modelCapabilityConfigFor(config, model);

    if (capability === "image" && profile.image) {
        const normalized = normalizeImageValue(profile.image, {
            size: source("size"),
            quality: source("quality"),
            count: source("count"),
            transparentBackground: source("transparentBackground"),
        });
        return normalized;
    }

    if (capability === "video" && profile.video) {
        const normalized = normalizeVideoValue(profile.video, {
            seconds: source("videoSeconds"),
            ratio: source("size"),
            resolution: source("vquality"),
        });
        return {
            videoSeconds: normalized.seconds,
            size: normalized.ratio,
            vquality: normalized.resolution.replace(/p$/i, ""),
            ...resolveModelVideoBooleanOptions(config, model, explicit, fallback),
        };
    }

    return {};
}

export function resolveModelVideoBooleanOptions(
    config: AiConfig,
    model: string,
    explicit: Partial<ModelVideoBooleanOptions> = {},
    _fallback: Partial<ModelVideoBooleanOptions> = {},
): ModelVideoBooleanOptions {
    const channel = resolveModelChannel(config, model);
    const cost = channel.modelCosts?.find((item) => item.model === modelOptionName(model));
    const profile = cost?.capabilityConfig?.video;
    const specs = cost?.logicalCapabilitySpec ? [cost.logicalCapabilitySpec] : cost?.logicalCapabilityProfiles || [];
    // 只以真实声明授权开关；保留旧 fallback 入参，但全局偏好和协议兜底不能补出模型能力。
    const resolve = (key: keyof ModelVideoBooleanOptions, profileKey: "generateAudio" | "watermark") => {
        if (profile) {
            const option = profile[profileKey];
            return option?.supported === true ? explicit[key] ?? String(option.default === true) : "false";
        }
        const values = specs.filter((spec) => spec.capability === "video").flatMap((spec) => spec.options?.[key]?.values || []);
        if (!values?.some(isTrueOption)) return "false";
        const initial = cost?.defaultOptions?.[key] ?? values.find((value) => typeof value === "boolean" || value === "true" || value === "false");
        return explicit[key] ?? String(isTrueOption(initial));
    };
    return {
        videoGenerateAudio: resolve("videoGenerateAudio", "generateAudio"),
        videoWatermark: resolve("videoWatermark", "watermark"),
    };
}

export function maxModelInputCapacity(config: AiConfig, capability: "image" | "video", kind: "image" | "video" | "audio") {
    const options = selectableModelsByCapability(config, capability);
    if (!options.length) return null;
    return options.reduce((maximum, model) => {
        const profile = modelCapabilityConfigFor(config, model);
        const value =
            capability === "image" ? (kind === "image" ? profile.image!.references.maxImages : 0) : kind === "image" ? profile.video!.references.maxImages : kind === "video" ? profile.video!.references.maxVideos : profile.video!.references.maxAudios;
        return Math.max(maximum, value);
    }, 0);
}

export function modelGroupReferenceLimits(config: AiConfig, selected: string, capability: ModelCapability, requirements?: ModelRequirements): ModelReferenceLimits | undefined {
    if (capability !== "image" && capability !== "video") return undefined;
    const options = selectableModelsByCapability(config, capability);
    const selectedGroup = groupModelsByDisplayName(config, options).find((group) => group.models.includes(selected));
    const groupModels = selectedGroup?.models || (selected ? [selected] : []);
    if (!groupModels.length) return undefined;
    const compatibleModels = requirements ? groupModels.filter((model) => !modelCompatibilityError(config, model, requirements)) : groupModels;
    const models = compatibleModels.length ? compatibleModels : groupModels;
    return models.reduce<ModelReferenceLimits>(
        (limits, model) => {
            const profile = modelCapabilityConfigFor(config, model);
            if (capability === "image") {
                return { ...limits, maxImages: Math.max(limits.maxImages, profile.image!.references.maxImages) };
            }
            const references = profile.video!.references;
            return {
                maxImages: Math.max(limits.maxImages, references.maxImages),
                maxVideos: Math.max(limits.maxVideos, references.maxVideos),
                maxAudios: Math.max(limits.maxAudios, references.maxAudios),
            };
        },
        { maxImages: 0, maxVideos: 0, maxAudios: 0 },
    );
}

export function inferVideoOperation(input: ModelInputSummary) {
    const visualInputCount = input.imageCount + input.characterCount;
    // 图片或角色决定图生视频主模式，音频只作为附加参考，不应把组合请求
    // 提升为全模态参考；纯音频输入才使用独立的 audio_to_video 能力。
    if (input.videoCount > 0 || visualInputCount > 2) return "reference_to_video";
    if (visualInputCount > 0) return "image_to_video";
    if (input.audioCount > 0) return "audio_to_video";
    return "text_to_video";
}

export function resolveVideoOperation(input: ModelInputSummary, storedOperation?: string) {
    if (storedOperation && !["text_to_video", "image_to_video", "audio_to_video", "extend", "reference_to_video"].includes(storedOperation)) return storedOperation;
    return inferVideoOperation(input);
}

export function resolvedVideoRequirementOperation(requirements?: ModelRequirements) {
    if (requirements?.videoOperationExplicit && requirements.videoOperation) return requirements.videoOperation;
    return requirements?.input ? resolveVideoOperation(requirements.input, requirements.videoOperation) : requirements?.videoOperation;
}

function videoOperationLabel(operation: string) {
    if (operation === "text_to_video") return "文生视频";
    if (operation === "image_to_video") return "图生视频";
    if (operation === "audio_to_video") return "音频生视频";
    if (operation === "reference_to_video") return "全模态参考";
    if (operation === "extend") return "视频续写";
    return "当前生成模式";
}
