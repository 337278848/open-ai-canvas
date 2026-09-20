import { modelCapabilityConfigFor } from "@/lib/model-capabilities";
import { assessModelApplicability } from "@/lib/model-applicability";
import { modelRequestOptions, resolveVideoOperation, type ModelRequirements } from "@/lib/model-selection";
import { videoCreationConfig, videoCreationDefaultSelection } from "@/lib/video-creation-admission";
import { isSeedanceVideoConfig } from "@/lib/seedance-video";
import type { CanvasVideoEditOperation } from "@/types/canvas";
import { resolveModelChannel, selectableModelsByCapability, type AiConfig } from "@/stores/use-config-store";

export type SegmentGenerationSettings = Pick<AiConfig, "videoSeconds" | "size" | "vquality" | "videoGenerateAudio" | "videoWatermark">;

// Each segment generates separately, with one extracted video as its reference.
// Source-video specifications are not output requirements.
export function buildCanvasVideoSegmentRequest(config: AiConfig, model: string, segments: Array<{ startMs: number; endMs: number }>, prompt: string, explicitOperation?: CanvasVideoEditOperation, settings?: SegmentGenerationSettings) {
    const requestConfig: AiConfig = { ...videoCreationConfig(config, model, settings), taskWorkflowProvider: "model" };
    const effectivePrompt = prompt.trim() || "保持画面主体与镜头，重新生成这一段视频";
    const input = { textCount: 1, imageCount: 0, videoCount: 1, audioCount: 0, characterCount: 0 };
    const profile = modelCapabilityConfigFor(config, model).video!;
    // Only expose operations the task constructor will send unchanged.
    const operations = profile.operations.filter((value) => resolveVideoOperation(input, value) === value) as CanvasVideoEditOperation[];
    const operation = explicitOperation ?? (operations.includes(profile.defaultOperation as CanvasVideoEditOperation)
        ? profile.defaultOperation as CanvasVideoEditOperation : operations[0] || "reference_to_video");
    const requirements: ModelRequirements = {
        capability: "video",
        input,
        prompt: effectivePrompt,
        videoOperation: operation,
        videoSeconds: requestConfig.videoSeconds,
        options: modelRequestOptions(requestConfig, "video"),
        media: [{ kind: "video", ...(segments.length ? { durationSeconds: Math.max(...segments.map((segment) => (segment.endMs - segment.startMs) / 1000)) } : {}) }],
    };
    const selectionRequirements = { ...videoCreationDefaultSelection(requirements), videoOperation: explicitOperation };
    const assessment = assessModelApplicability(config, model, requirements);
    const invalidRange = segments.some((segment) => !Number.isFinite(segment.startMs) || !Number.isFinite(segment.endMs) || segment.startMs < 0 || segment.endMs - segment.startMs < 100);
    const error = !segments.length ? "请至少添加一个截取片段"
        : invalidRange ? "片段范围无效，时长至少 0.1 秒"
        : !model ? "请选择视频模型"
        : resolveVideoOperation(input, operation) !== operation ? "所选生成模式与实际参考视频请求不一致，请重新选择"
        : assessment.status !== "ready" ? assessment.reason || "当前模型不适用于这些片段"
        : validateVideoSegmentBatch(requestConfig, segments, operation);
    const generationSettings: SegmentGenerationSettings = {
        videoSeconds: requestConfig.videoSeconds,
        size: requestConfig.size,
        vquality: requestConfig.vquality,
        videoGenerateAudio: requestConfig.videoGenerateAudio,
        videoWatermark: requestConfig.videoWatermark,
    };
    return { config: requestConfig, prompt: effectivePrompt, operation, operations, requirements, selectionRequirements, generationSettings, assessment, error };
}

export function listVideoReferenceModels(config: AiConfig): string[] {
    return selectableModelsByCapability(config, "video").filter((model) => {
        const profile = modelCapabilityConfigFor(config, model).video;
        if (!profile || profile.references.maxVideos < 1 || !profile.operations.length) return false;
        const channel = resolveModelChannel(config, model);
        return Boolean(channel.baseUrl.trim() && channel.apiKey.trim());
    });
}

export function videoReferenceRegenerationError(config: AiConfig): string {
    const videoProfile = modelCapabilityConfigFor(config, config.model).video;
    if (!videoProfile || videoProfile.references.maxVideos < 1) {
        return "当前所选视频模型不支持参考视频，无法为片段创建待生成节点。请选择支持参考视频的模型，或在设置中配置 Seedance / Agent Plan / NewAPI 渠道。";
    }
    if (!videoProfile.operations.length) return "当前视频模型没有可用的视频生成模式";
    return "";
}

export function videoReferenceOperationError(config: AiConfig, operation: CanvasVideoEditOperation): string {
    const videoProfile = modelCapabilityConfigFor(config, config.model).video;
    if (!videoProfile?.operations.includes(operation)) return "当前视频模型不支持所选生成模式";
    return "";
}

export function validateVideoSegmentBatch(config: AiConfig, segments: Array<{ startMs: number; endMs: number }>, operation?: CanvasVideoEditOperation): string {
    const referenceError = videoReferenceRegenerationError(config);
    if (referenceError) return referenceError;
    if (operation) {
        const operationError = videoReferenceOperationError(config, operation);
        if (operationError) return operationError;
    }
    for (const segment of segments) {
        const segmentError = videoReferenceSegmentError(config, segment.endMs - segment.startMs);
        if (segmentError) return segmentError;
    }
    return "";
}

export function videoReferenceSegmentError(config: AiConfig, durationMs: number): string {
    const videoProfile = modelCapabilityConfigFor(config, config.model).video;
    const maxSeconds = videoProfile?.references.maxVideoDurationSeconds || 0;
    if (maxSeconds > 0 && durationMs > maxSeconds * 1000) {
        return `截取片段不能超过当前模型参考视频上限（${maxSeconds} 秒）`;
    }
    if (isSeedanceVideoConfig(config) && durationMs < 2000) {
        return "Seedance 参考视频单段至少 2 秒，请扩大截取范围";
    }
    return "";
}
