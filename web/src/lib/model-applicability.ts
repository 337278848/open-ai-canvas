import { normalizeCapabilityString, type VideoCapabilityConfig } from "@/lib/model-capabilities";
import { priceTiersForCurrentSelection, requestCreditCost } from "@/lib/model-pricing";
import { isVolcengineArkVideoProtocol } from "@/lib/model-protocols";
import { modelCompatibilityError, type ModelInputSummary, type ModelRequirements } from "@/lib/model-selection";
import type { CapabilitySpec, OptionConstraint } from "@/services/api/logical-models";
import { decodeChannelModel, filterModelsByCapability, normalizeModelOptionValue, type AiConfig, type ModelChannel } from "@/stores/use-config-store";

export type ModelApplicability = {
    status: "ready" | "needs_input" | "incompatible" | "unknown";
    reason: string;
    summary: string[];
    estimatedCredits?: number;
};

type ModelCost = NonNullable<ModelChannel["modelCosts"]>[number];
type VideoSelection = ModelRequirements & { capability: "video"; input: ModelInputSummary; options: Record<string, unknown> };

const videoOptionAliases: Record<string, string[]> = {
    videoSeconds: ["videoSeconds", "duration"],
    size: ["size", "aspectRatio"],
    vquality: ["vquality", "resolution"],
    videoGenerateAudio: ["videoGenerateAudio"],
    videoWatermark: ["videoWatermark"],
};

function modelEntry(config: AiConfig, model: string) {
    const value = normalizeModelOptionValue(model, config.channels);
    const decoded = decodeChannelModel(value);
    const channel = config.channels.find((item) => item.id === decoded?.channelId);
    const cost = channel?.modelCosts?.find((item) => item.model === decoded?.model);
    return { value, channel, cost };
}

function logicalProfiles(cost?: ModelCost): CapabilitySpec[] {
    return cost?.logicalCapabilityProfiles?.length ? cost.logicalCapabilityProfiles : cost?.logicalCapabilitySpec ? [cost.logicalCapabilitySpec] : [];
}

function specified(value: unknown) {
    return value !== undefined && value !== null && value !== "";
}

function optionDefault(constraint?: OptionConstraint) {
    return constraint?.values?.find((value) => specified(value) && value !== "*") ?? constraint?.min;
}

// Defaults come only from this catalogue entry, never the previously selected model.
function selectionOptions(cost: ModelCost | undefined, requirements?: ModelRequirements) {
    const video = cost?.capabilityConfig?.video;
    const specs = logicalProfiles(cost);
    const spec = cost?.logicalCapabilitySpec || specs[0];
    const profileDefaults: Record<string, unknown> = {
        videoSeconds: video?.duration?.default,
        size: video?.ratios?.length ? video.defaultRatio : undefined,
        vquality: video?.resolutions?.length ? video.defaultResolution : undefined,
        videoGenerateAudio: video?.generateAudio?.supported === false ? false : video?.generateAudio?.default,
        videoWatermark: video?.watermark?.supported === false ? false : video?.watermark?.default,
    };
    const declaredDefaults: Record<string, unknown> = specs.length
        ? Object.fromEntries(Object.entries(spec?.options || {}).map(([key, constraint]) => {
              const canonical = Object.keys(videoOptionAliases).find((name) => videoOptionAliases[name].includes(key)) || key;
              return [key, profileDefaults[canonical] ?? optionDefault(constraint)];
          }))
        : profileDefaults;
    for (const [key, value] of Object.entries(declaredDefaults)) {
        if (typeof value === "string") declaredDefaults[key] = normalizeCapabilityString(value);
    }
    const defaults = { ...declaredDefaults, ...cost?.defaultOptions };
    const explicit = requirements?.options || {};
    const options: Record<string, unknown> = Object.fromEntries(Object.entries({ ...defaults, ...explicit }).filter(([, value]) => specified(value)));
    for (const [key, aliases] of Object.entries(videoOptionAliases)) {
        const requested = aliases.map((alias) => explicit[alias]).find(specified);
        const initial = aliases.map((alias) => defaults[alias]).find(specified);
        if (specified(requested ?? initial)) options[key] = requested ?? initial;
    }
    if (specified(requirements?.videoSeconds)) options.videoSeconds = requirements!.videoSeconds;
    return options;
}

function inputSummary(requirements?: ModelRequirements): ModelInputSummary {
    if (requirements?.input) return { ...requirements.input };
    const media = requirements?.media || [];
    return {
        textCount: requirements?.prompt ? 1 : 0,
        imageCount: media.filter((item) => item.kind === "image").length,
        videoCount: media.filter((item) => item.kind === "video").length,
        audioCount: media.filter((item) => item.kind === "audio").length,
        characterCount: 0,
    };
}

function videoSelection(cost?: ModelCost, requirements?: ModelRequirements): VideoSelection {
    const options = selectionOptions(cost, requirements);
    return {
        ...requirements,
        capability: "video",
        videoOperationExplicit: requirements?.videoOperationExplicit ?? Boolean(requirements?.videoOperation),
        input: inputSummary(requirements),
        options,
        videoSeconds: specified(options.videoSeconds) ? String(options.videoSeconds) : undefined,
    };
}

function selectionConfig(config: AiConfig, model: string, selection: VideoSelection): AiConfig {
    const value = (name: string) => specified(selection.options[name]) ? String(selection.options[name]) : "";
    return {
        ...config,
        model,
        videoModel: model,
        videoSeconds: value("videoSeconds"),
        size: value("size"),
        vquality: value("vquality"),
        videoGenerateAudio: value("videoGenerateAudio"),
        videoWatermark: value("videoWatermark"),
        count: "1",
    };
}

/** A request-local snapshot for candidate video previews; does not read or update stores. */
export function buildModelApplicabilityConfig(config: AiConfig, model: string, requirements?: ModelRequirements): AiConfig {
    const { value, cost } = modelEntry(config, model);
    if ((requirements?.capability || cost?.capability || "video") !== "video") return { ...config, model: value || model };
    return selectionConfig(config, value || model, videoSelection(cost, requirements));
}

export function assessModelApplicability(config: AiConfig, model: string, requirements?: ModelRequirements): ModelApplicability {
    const { value, channel, cost } = modelEntry(config, model);
    const capability = requirements?.capability || cost?.capability || "video";
    if (capability !== "video") {
        const reason = modelCompatibilityError(config, model, { ...requirements, capability });
        return { status: reason ? "incompatible" : "ready", reason: reason || "符合当前输入要求", summary: [] };
    }

    const selection = videoSelection(cost, requirements);
    const summary = videoSummary(cost, selection);
    const adapterRule = channel ? videoAdapterAudioRule(channel, cost) : undefined;
    if (adapterRule) summary.push(`组合约束：${adapterRule.reason}`);
    const result = (status: ModelApplicability["status"], reason: string, estimatedCredits?: number): ModelApplicability => ({
        status, reason, summary, ...(estimatedCredits === undefined ? {} : { estimatedCredits }),
    });
    if (!value || !channel || !config.videoModels.includes(value) || !filterModelsByCapability([value], "video", config.channels).length) {
        return result("incompatible", "模型不在当前可用视频列表中");
    }
    if (channel.enabled === false) return result("incompatible", "此模型渠道已禁用");

    const specs = logicalProfiles(cost);
    const video = cost?.capabilityConfig?.video;
    if (!specs.length && !video) return result("unknown", "目录未声明视频能力，无法确认是否适用");
    if (!specs.length && !completeVideoDeclaration(video)) return result("unknown", "目录的视频能力声明不完整，无法确认是否适用");

    const candidateConfig = selectionConfig(config, value, selection);
    const error = videoAssessmentError(candidateConfig, value, cost, selection, adapterRule);
    if (!error) {
        const price = selectionPrice(candidateConfig, channel, cost, selection);
        return price.missing ? result("incompatible", "当前规格没有可匹配的售价")
            : result("ready", "符合当前输入与参数要求", price.estimatedCredits);
    }

    // Only a successful recheck of the whole request can turn an error into a
    // missing-input suggestion. Do not reinterpret an explicitly requested mode.
    let filledError = "";
    if (!selection.videoOperationExplicit) {
        const usedImages = selection.input.imageCount + selection.input.characterCount;
        const targets = missingImageTargets(cost, selection);
        for (const target of targets) {
            const filled: VideoSelection = { ...selection, input: { ...selection.input, imageCount: selection.input.imageCount + target - usedImages } };
            const candidateError = videoAssessmentError(candidateConfig, value, cost, filled, adapterRule);
            if (candidateError) {
                filledError ||= candidateError;
                continue;
            }
            const price = selectionPrice(candidateConfig, channel, cost, filled);
            if (price.missing) {
                filledError = "补图后的规格没有可匹配的售价";
                continue;
            }
            return result("needs_input", `还需 ${target - usedImages} 张参考图（当前 ${usedImages} 张，补齐至 ${target} 张后符合要求）`, price.estimatedCredits);
        }
    }
    return result("incompatible", filledError || error);
}

function videoAdapterAudioRule(channel: ModelChannel, cost?: ModelCost) {
    // Match the selected model's explicit transport, not its display name, URL
    // or another route. A system channel can hide several distinct protocols.
    const protocol = cost?.protocol || (channel.scope !== "system" ? channel.interfaceType : undefined);
    // video-provider-seedance.ts/buildSeedanceAgentPlanPayload and
    // backend/internal/app/model_capability.go/validateVideoTask.
    if (isVolcengineArkVideoProtocol(protocol)) return {
        requiresVideo: false,
        reason: "火山方舟全模态参考不支持纯音频或文本+音频，请同时添加参考图片或参考视频",
    };
    // video-provider-newapi.ts/createVideoGenerationsTask: an image is not a
    // substitute for the companion video required by this particular adapter.
    if (protocol === "newapi-channel-2") return {
        requiresVideo: true,
        reason: "NewAPI Video Generations 的参考音频必须同时提供至少 1 个参考视频；纯音频生视频请切换到支持该模式的渠道",
    };
}

function videoAssessmentError(config: AiConfig, model: string, cost: ModelCost | undefined, selection: VideoSelection, adapterRule: ReturnType<typeof videoAdapterAudioRule>) {
    const error = modelCompatibilityError(config, model, selection);
    if (error) return error;
    if (adapterRule && selection.input.audioCount > 0) {
        const companions = selection.input.videoCount + (adapterRule.requiresVideo ? 0 : selection.input.imageCount + selection.input.characterCount);
        if (companions === 0) return adapterRule.reason;
    }
    const specs = logicalProfiles(cost);
    if (specs.length) {
        const publicOptions = cost?.logicalCapabilitySpec?.options || Object.assign({}, ...specs.map((spec) => spec.options));
        // Core matching deliberately filters non-public logical options. An
        // enabled output switch is not a harmless no-op and cannot be dropped.
        for (const [name, label] of [["videoGenerateAudio", "生成声音"], ["videoWatermark", "水印"]] as const) {
            if (selection.options[name] !== true && selection.options[name] !== "true") continue;
            if (!publicOptions[name]) return `目录未声明${label}开关，无法满足当前已开启的设置`;
            const concreteOption = name === "videoGenerateAudio" ? cost?.capabilityConfig?.video?.generateAudio : cost?.capabilityConfig?.video?.watermark;
            if (concreteOption?.supported === false) return `渠道能力不支持开启${label}，请调整设置`;
        }
    }
    return "";
}

function finiteNonnegative(value: unknown): value is number {
    return typeof value === "number" && Number.isFinite(value) && value >= 0;
}

function completeVideoDeclaration(video?: VideoCapabilityConfig) {
    if (!video || !Array.isArray(video.operations) || !video.operations.length || !video.operations.every((value) => typeof value === "string" && value.trim())) return false;
    for (const [values, initial] of [[video.ratios, video.defaultRatio], [video.resolutions, video.defaultResolution]] as const) {
        if (!Array.isArray(values) || !values.every((value) => typeof value === "string" && value.trim())) return false;
        if (values.length && (typeof initial !== "string" || !initial.trim())) return false;
    }
    const references = video.references;
    const keys: Array<keyof VideoCapabilityConfig["references"]> = ["minImages", "maxImages", "maxVideos", "maxAudios", "maxImageBytes", "maxVideoBytes", "maxAudioBytes", "maxVideoDurationSeconds", "maxAudioDurationSeconds", "promptMaxChars"];
    if (!references || keys.some((key) => !finiteNonnegative(references[key]))) return false;
    if (!["minImages", "maxImages", "maxVideos", "maxAudios"].every((key) => Number.isInteger(references[key as keyof typeof references]))) return false;
    const duration = video.duration;
    if (!duration || !finiteNonnegative(duration.default) || duration.default <= 0) return false;
    if (duration.selection === "enum") {
        if (!duration.values?.length || !duration.values.every((value) => finiteNonnegative(value) && value > 0)) return false;
    } else if (duration.selection !== "range" || !finiteNonnegative(duration.min) || !finiteNonnegative(duration.max) || !finiteNonnegative(duration.step) || duration.step <= 0 || duration.min <= 0 || duration.max < duration.min) return false;
    return [video.generateAudio, video.watermark].every((option) => typeof option?.supported === "boolean" && typeof option.default === "boolean");
}

function missingImageTargets(cost: ModelCost | undefined, selection: VideoSelection) {
    const used = selection.input.imageCount + selection.input.characterCount;
    const noMedia = used === 0 && selection.input.videoCount === 0 && selection.input.audioCount === 0;
    const specs = logicalProfiles(cost);
    const profiles = specs.length
        ? specs.filter((spec) => spec.capability === "video").map((spec) => ({ min: spec.inputs?.image?.min, max: spec.inputs?.image?.max, operations: spec.operations }))
        : [{ min: cost?.capabilityConfig?.video?.references?.minImages, max: cost?.capabilityConfig?.video?.references?.maxImages, operations: cost?.capabilityConfig?.video?.operations }];
    const targets = profiles.flatMap(({ min, max, operations }) => {
        const target = Math.max(min ?? 0, noMedia && operations?.includes("image_to_video") ? 1 : 0);
        return Number.isInteger(target) && target > used && finiteNonnegative(max) && target <= max ? [target] : [];
    });
    return [...new Set(targets)].sort((left, right) => left - right);
}

function configuredPrice(cost: Pick<ModelCost, "billingMode" | "unitPriceMicrocredits" | "inputTokenPriceMicrocredits" | "outputTokenPriceMicrocredits" | "cachedTokenPriceMicrocredits">) {
    if (cost.billingMode === "token") return [cost.inputTokenPriceMicrocredits, cost.outputTokenPriceMicrocredits, cost.cachedTokenPriceMicrocredits].every(finiteNonnegative);
    return ["fixed_request", "per_second"].includes(cost.billingMode) && finiteNonnegative(cost.unitPriceMicrocredits);
}

function selectionPrice(config: AiConfig, channel: ModelChannel, cost: ModelCost | undefined, requirements: VideoSelection): { missing: boolean; estimatedCredits?: number } {
    const system = channel.scope === "system";
    if (!cost || (!system && !cost.pricePolicy)) return { missing: system };
    const useTiers = cost.pricePolicy === "channel" || (!cost.pricePolicy && Boolean(cost.logicalPriceTiers?.length));
    // An absent logical audio option is unknown, not a request for audio=false.
    const pricingRequirements = { ...requirements, options: { videoGenerateAudio: undefined, ...requirements.options } };
    const tiers = useTiers ? priceTiersForCurrentSelection(cost.logicalPriceTiers || [], "video", config, pricingRequirements) : [];
    const priced = useTiers ? tiers[0] : cost;
    if (!priced || !(useTiers ? tiers.every(configuredPrice) : configuredPrice(cost))) return { missing: system };
    const perRequest = requestCreditCost({
        channelMode: config.channelMode,
        modelCosts: [{ ...cost, ...(useTiers ? { pricePolicy: "channel" as const } : {}) }],
        model: cost.model,
        count: 1,
        seconds: 1,
        capability: "video",
        config,
        requirements: pricingRequirements,
    });
    if (perRequest === null || !finiteNonnegative(perRequest)) return { missing: false };
    const seconds = Number(requirements.videoSeconds);
    // Ask the shared pricing helper for one unit, then retain fractional seconds.
    const total = priced.billingMode === "per_second" ? seconds > 0 && Number.isFinite(seconds) ? perRequest * seconds : undefined : perRequest;
    return { missing: false, ...(finiteNonnegative(total) ? { estimatedCredits: total } : {}) };
}

const operationLabels: Record<string, string> = {
    text_to_video: "文生视频",
    image_to_video: "图生视频",
    reference_to_video: "多模态参考",
    audio_to_video: "音频生视频",
    video_to_video: "视频生视频",
    extend: "视频续写",
    concat: "视频拼接",
};

function declaredStrings(values?: string[]) {
    return Array.isArray(values) ? values.filter((value) => typeof value === "string").map(normalizeCapabilityString).filter(Boolean) : [];
}

function constraintLabel(constraint?: OptionConstraint) {
    if (constraint?.values?.length) return constraint.values.map(String).join(" / ");
    if (constraint?.min !== undefined && constraint.max !== undefined) return `${constraint.min}–${constraint.max}${constraint.step !== undefined ? `（步长 ${constraint.step}）` : ""}`;
    return "";
}

function videoSummary(cost: ModelCost | undefined, selection: VideoSelection) {
    const video = cost?.capabilityConfig?.video;
    const specs = logicalProfiles(cost).filter((spec) => spec.capability === "video");
    const lines: string[] = [];
    const operations = [...new Set(specs.length ? specs.flatMap((spec) => declaredStrings(spec.operations)) : declaredStrings(video?.operations))];
    if (operations.length) lines.push(`模式：${operations.map((value) => operationLabels[value] || value).join(" / ")}`);
    const used = { image: selection.input.imageCount + selection.input.characterCount, video: selection.input.videoCount, audio: selection.input.audioCount };
    const references = video?.references;
    for (const [kind, label, unit, key] of [["image", "图片", "张", "maxImages"], ["video", "视频", "个", "maxVideos"], ["audio", "音频", "个", "maxAudios"]] as const) {
        if (specs.length) {
            const descriptions = specs.map((spec) => {
                const constraint = spec.inputs?.[kind];
                return constraint ? `上限 ${constraint.max} ${unit}，最少 ${constraint.min} ${unit}` : "目录未声明此输入";
            });
            lines.push(`${label}：已用 ${used[kind]} ${unit}；${profileLabels(descriptions)}`);
        } else if (finiteNonnegative(references?.[key])) {
            lines.push(`${label}：已用 ${used[kind]} / 上限 ${references[key]} ${unit}${kind === "image" && finiteNonnegative(references.minImages) ? `；最少 ${references.minImages} 张` : ""}`);
        }
    }

    const optionLabels = (names: string[]) => specs.length ? profileLabels(specs.map((spec) => constraintLabel(names.map((name) => spec.options?.[name]).find(Boolean))), false) : "";
    const duration = specs.length ? optionLabels(["videoSeconds", "duration"]) : video?.duration?.selection === "enum"
        ? video.duration.values?.join(" / ") || ""
        : constraintLabel(video?.duration);
    const resolutions = specs.length ? optionLabels(["vquality", "resolution"]) : declaredStrings(video?.resolutions).join(" / ");
    const selected = (name: string) => specified(selection.options[name]) ? `；本次 ${selection.options[name]}` : "";
    const technical = [duration ? `时长：${duration} 秒${selected("videoSeconds")}${specified(selection.options.videoSeconds) ? " 秒" : ""}` : "", resolutions ? `清晰度：${resolutions}${selected("vquality")}` : ""].filter(Boolean);
    if (technical.length) lines.push(technical.join("；"));
    const ratios = specs.length ? optionLabels(["size", "aspectRatio"]) : declaredStrings(video?.ratios).join(" / ");
    if (ratios) lines.push(`比例：${ratios}${selected("size")}`);
    const logicalAudio = specs.length ? optionLabels(["videoGenerateAudio"]) : "";
    if (logicalAudio) lines.push(`生成声音开关：${logicalAudio}；开关取值不保证成片一定有声或无声`);
    else if (!specs.length && typeof video?.generateAudio?.supported === "boolean") lines.push(`生成声音开关：${video.generateAudio.supported ? "支持切换" : "不支持手动切换"}；不代表成片一定有声或无声`);

    const sizes = [["图片", references?.maxImageBytes], ["视频", references?.maxVideoBytes], ["音频", references?.maxAudioBytes]] as const;
    const sizeLabels = sizes.filter((item): item is readonly [typeof item[0], number] => finiteNonnegative(item[1]) && item[1] > 0).map(([label, bytes]) => `${label} ≤ ${Number((bytes / 1024 / 1024).toFixed(2))} MB`);
    if (sizeLabels.length) lines.push(`单文件大小：${sizeLabels.join("；")}`);
    const durations = [["视频", references?.maxVideoDurationSeconds], ["音频", references?.maxAudioDurationSeconds]] as const;
    const durationLabels = durations.filter((item): item is readonly [typeof item[0], number] => finiteNonnegative(item[1]) && item[1] > 0).map(([label, seconds]) => `${label} ≤ ${seconds} 秒`);
    if (durationLabels.length) lines.push(`参考素材单个时长：${durationLabels.join("；")}`);
    if (finiteNonnegative(references?.promptMaxChars) && references.promptMaxChars > 0) lines.push(`提示词：${selection.prompt !== undefined ? `已用 ${Array.from(selection.prompt).length} / ` : ""}上限 ${references.promptMaxChars} 字符`);
    lines.push("首尾帧：目录未声明首尾帧边界，不能仅凭两张图片确认支持");
    if (specs.length > 1) lines.push("须满足同一方案内的全部限制，不能跨方案拼接参数");
    return lines;
}

function profileLabels(labels: string[], showMissing = true) {
    const unique = [...new Set(labels)];
    if (unique.length === 1) return unique[0];
    if (!showMissing && !labels.some(Boolean)) return "";
    return labels.map((label, index) => `方案 ${index + 1}：${label || "未声明"}`).join("；");
}
