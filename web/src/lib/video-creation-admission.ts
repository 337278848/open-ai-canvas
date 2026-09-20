import { assessModelApplicability, buildModelApplicabilityConfig } from "@/lib/model-applicability";
import { inferVideoOperation, modelRequestOptions, resolveCompatibleModel, resolveVideoOperation, type ModelGenerationDefaults, type ModelRequirements } from "@/lib/model-selection";
import { normalizeVideoResolution } from "@/lib/video-generation-options";
import type { AiConfig } from "@/stores/use-config-store";

type VideoCreationOptions = Pick<ModelGenerationDefaults, "size" | "videoSeconds" | "vquality" | "videoGenerateAudio" | "videoWatermark">;

export type VideoCreationRequirements = ModelRequirements & {
    /** Execution may pin an inferred operation; discovery must not treat that as user intent. */
    videoOperationOrigin?: "user" | "inferred";
};

// Only unset preferences may use model defaults. Explicit selections must reach
// admission unchanged, including selections that the current model cannot serve.
export function videoCreationConfig(config: AiConfig, model: string, explicit: Partial<VideoCreationOptions> = {}): AiConfig {
    const defaults = buildModelApplicabilityConfig(config, model, { capability: "video" });
    return {
        ...config,
        model: defaults.model || model,
        videoModel: defaults.model || model,
        size: explicit.size ?? defaults.size ?? config.size,
        videoSeconds: explicit.videoSeconds ?? defaults.videoSeconds ?? config.videoSeconds,
        vquality: explicit.vquality ?? normalizeVideoResolution(defaults.vquality),
        videoGenerateAudio: explicit.videoGenerateAudio ?? defaults.videoGenerateAudio ?? config.videoGenerateAudio,
        videoWatermark: explicit.videoWatermark ?? defaults.videoWatermark ?? config.videoWatermark,
    };
}

export function videoCreationAdmission(config: AiConfig, requirements: VideoCreationRequirements, routeWithinGroup = false) {
    const videoOperation = requirements.videoOperationExplicit
        ? requirements.videoOperation
        : requirements.input ? resolveVideoOperation(requirements.input, requirements.videoOperation) : requirements.videoOperation;
    const videoOperationOrigin = requirements.videoOperationOrigin ?? (
        requirements.videoOperation && (requirements.videoOperationExplicit || !requirements.input || videoOperation !== inferVideoOperation(requirements.input))
            ? "user" : "inferred"
    );
    const requestRequirements: VideoCreationRequirements = {
        ...requirements,
        capability: "video",
        videoOperation,
        videoOperationExplicit: true,
        videoOperationOrigin,
        videoSeconds: config.videoSeconds,
        options: modelRequestOptions(config, "video"),
    };
    const selectionRequirements = videoCreationDiscoveryRequirements(requestRequirements);
    const assessment = assessModelApplicability(config, config.model, selectionRequirements);
    // A compatible concrete choice is intentional; billing units are not comparable.
    const compatibleModel = routeWithinGroup && assessment.status !== "ready" && assessment.status !== "unknown" ? resolveCompatibleModel(config, config.model, requestRequirements) : config.model;
    const routeChanged = Boolean(compatibleModel && compatibleModel !== config.model);
    const suggestedModel = routeChanged && assessModelApplicability(config, compatibleModel, requestRequirements).status === "ready" ? compatibleModel : "";
    const error = assessment.status !== "ready"
        ? assessment.reason || "当前模型能力尚未确认，请选择已声明能力的模型"
        : "";
    return { requirements: requestRequirements, selectionRequirements, assessment, suggestedModel, error };
}

export function videoCreationDiscoveryRequirements(requirements: VideoCreationRequirements): VideoCreationRequirements {
    return requirements.videoOperationOrigin === "inferred"
        ? { ...requirements, videoOperation: undefined, videoOperationExplicit: false }
        : { ...requirements };
}

// Only an explicit reset action may remove specifications from discovery.
// Input, user-chosen operations and media limits still apply with new defaults.
export function videoCreationDefaultSelection(requirements: VideoCreationRequirements): VideoCreationRequirements {
    return { ...videoCreationDiscoveryRequirements(requirements), videoSeconds: undefined, imageSize: undefined, options: undefined };
}

// A model-only canvas patch clears saved parameters. Carry the displayed values
// in the same patch so confirmation and submission cannot silently reset them.
export function videoCreationNodePatch(config: AiConfig, model = config.model) {
    return {
        model,
        ...videoCreationOptionsPatch(config),
    };
}

// Submission synchronizes displayed specifications without changing the
// canonical model selection. Only an explicit picker action may switch a
// logical selection to a concrete channel model.
export function videoCreationOptionsPatch(config: AiConfig) {
    return {
        size: config.size,
        seconds: config.videoSeconds,
        vquality: config.vquality,
        generateAudio: config.videoGenerateAudio,
        watermark: config.videoWatermark,
    };
}
