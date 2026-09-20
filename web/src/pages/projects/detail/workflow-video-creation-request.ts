import { modelRequestOptions, resolveVideoOperation } from "@/lib/model-selection";
import type { VideoCreationRequirements } from "@/lib/video-creation-admission";
import type { AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";
import type { ReferenceAudio } from "@/types/media";

export function workflowVideoCreationRequirements(
    config: AiConfig,
    referenceImages: ReferenceImage[],
    referenceAudios: ReferenceAudio[],
    prompt?: string,
): VideoCreationRequirements {
    const input = {
        textCount: prompt?.trim() ? 1 : 0,
        imageCount: referenceImages.length,
        videoCount: 0,
        audioCount: referenceAudios.length,
        characterCount: 0,
    };
    return {
        capability: "video",
        input,
        videoOperation: resolveVideoOperation(input),
        videoOperationExplicit: true,
        videoOperationOrigin: "inferred",
        videoSeconds: config.videoSeconds,
        options: modelRequestOptions(config, "video"),
        prompt,
        media: [
            ...referenceImages.map((image) => ({ kind: "image" as const, ...(image.bytes === undefined ? {} : { bytes: image.bytes }) })),
            ...referenceAudios.map((audio) => ({
                kind: "audio" as const,
                ...(audio.bytes === undefined ? {} : { bytes: audio.bytes }),
                ...(audio.durationMs === undefined ? {} : { durationSeconds: audio.durationMs / 1000 }),
            })),
        ],
    };
}
