import type { ModelRequirements } from "@/lib/model-selection";
import { videoCreationConfig } from "@/lib/video-creation-admission";
import type { AiConfig } from "@/stores/use-config-store";
import type { CanvasNodeData } from "@/types/canvas";

// Interactive generation and retries must validate the saved request before
// the legacy executor can normalize unsupported settings or route another model.
export function buildCanvasVideoRequestConfig(config: AiConfig, node: CanvasNodeData | undefined, runtimeConfig = config): AiConfig {
    const metadata = node?.metadata;
    return {
        ...videoCreationConfig(runtimeConfig, metadata?.model || config.videoModel || config.model, {
            size: metadata?.size,
            videoSeconds: metadata?.seconds,
            vquality: metadata?.vquality,
            videoGenerateAudio: metadata?.generateAudio,
            videoWatermark: metadata?.watermark,
        }),
        taskWorkflowProvider: "model",
    };
}

type ReferenceMetadata = { id?: string; bytes?: number; durationMs?: number };

export function canvasVideoMediaMetadata(input: {
    referenceImages?: ReferenceMetadata[];
    referenceVideos?: ReferenceMetadata[];
    referenceAudios?: ReferenceMetadata[];
}): ModelRequirements["media"] {
    return [
        ...(input.referenceImages || []).map((item) => ({ kind: "image" as const, bytes: item.bytes })),
        ...(input.referenceVideos || []).map((item) => ({ kind: "video" as const, bytes: item.bytes, durationSeconds: item.durationMs === undefined ? undefined : item.durationMs / 1000 })),
        ...(input.referenceAudios || []).map((item) => ({ kind: "audio" as const, bytes: item.bytes, durationSeconds: item.durationMs === undefined ? undefined : item.durationMs / 1000 })),
    ];
}
