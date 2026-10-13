import { createSecureId } from "@/lib/client-id";
import { isGenerationTaskSubmissionUncertain } from "@/lib/generation-task-display";
import { videoCreationAdmission, videoCreationConfig } from "@/lib/video-creation-admission";
import { createCreationSubmitGate } from "@/pages/create/creation-submit-gate";
import type { GenerationTask } from "@/services/api/task-center";
import type { WorkbenchKind, WorkbenchTaskReceipt } from "@/services/api/workbench";
import type { AiConfig } from "@/stores/use-config-store";

export type WorkbenchReference = {
    id: string;
    name: string;
    kind: "image" | "video" | "audio";
    resourceId?: string;
    previewUrl: string;
    progress: number;
    uploading: boolean;
    error?: string;
    bytes?: number;
    role: "reference" | "first_frame" | "last_frame";
    slotKey?: string;
};

export function workbenchUploadReference(file: File, slotKey?: string): WorkbenchReference {
    return {
        id: createSecureId(),
        name: file.name,
        kind: file.type.startsWith("image/") ? "image" : file.type.startsWith("video/") ? "video" : "audio",
        previewUrl: URL.createObjectURL(file),
        progress: 0,
        uploading: true,
        bytes: file.size,
        role: "reference",
        ...(slotKey ? { slotKey } : {}),
    };
}

type ReferenceSnapshot = Pick<WorkbenchReference, "kind" | "slotKey"> & { resourceId: string } & Partial<Pick<WorkbenchReference, "role">>;

export async function restoreWorkbenchReferences(references: ReferenceSnapshot[], resolveURL: (resourceId: string) => Promise<string | undefined>): Promise<WorkbenchReference[]> {
    return Promise.all(references.map(async (reference, index) => ({
        id: createSecureId(),
        name: `${reference.kind === "image" ? "图片" : reference.kind === "video" ? "视频" : "音频"} ${index + 1}`,
        kind: reference.kind,
        resourceId: reference.resourceId,
        previewUrl: (await resolveURL(reference.resourceId)) || "",
        progress: 100,
        uploading: false,
        role: reference.role || "reference",
        ...(reference.slotKey ? { slotKey: reference.slotKey } : {}),
    })));
}

export function workbenchVideoRequest(
    config: AiConfig,
    model: string,
    explicit: Parameters<typeof videoCreationConfig>[2],
    prompt: string,
    references: Array<Pick<WorkbenchReference, "kind" | "bytes">>,
) {
    const generationConfig = videoCreationConfig(config, model, explicit);
    const admission = videoCreationAdmission(generationConfig, {
        capability: "video",
        prompt,
        input: {
            textCount: prompt.trim() ? 1 : 0,
            imageCount: references.filter((reference) => reference.kind === "image").length,
            videoCount: references.filter((reference) => reference.kind === "video").length,
            audioCount: references.filter((reference) => reference.kind === "audio").length,
            characterCount: 0,
        },
        media: references.map((reference) => ({ kind: reference.kind, ...(reference.bytes === undefined ? {} : { bytes: reference.bytes }) })),
    });
    return { config: generationConfig, ...admission };
}

export function canRetryWorkbenchTask(task: GenerationTask) {
    return (task.status === "failed" || task.status === "cancelled") && !isGenerationTaskSubmissionUncertain(task);
}

type ReceiptContext = Pick<GenerationTask, "prompt" | "model" | "inputJson"> & { kind: WorkbenchKind };
type TaskPublisher = (task: GenerationTask, accepted: boolean) => void;
type TaskQuery = (id: string) => Promise<GenerationTask>;

export function mergeWorkbenchTaskList(current: GenerationTask[], incoming: GenerationTask[]) {
    const ids = new Set(incoming.map((task) => task.id));
    // A list requested before creation must not erase the accepted receipt.
    return [...current.filter((task) => !ids.has(task.id)), ...incoming.map((task) => ({
        ...task,
        inputJson: task.inputJson || current.find((item) => item.id === task.id)?.inputJson,
    }))].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt)).slice(0, 100);
}

export function createWorkbenchSubmissionController() {
    const gate = createCreationSubmitGate();
    const refresh = async (task: GenerationTask, query: TaskQuery, publish: TaskPublisher) => {
        try {
            const detail = await query(task.id);
            publish(detail, false);
            return { task: detail, detailError: null };
        } catch (detailError) {
            return { task, detailError };
        }
    };
    return {
        refresh,
        async submit(create: () => Promise<WorkbenchTaskReceipt["task"] | GenerationTask>, context: ReceiptContext, query: TaskQuery, publish: TaskPublisher) {
            const token = gate.tryAcquire();
            if (!token) return { status: "busy" as const };
            let task: GenerationTask;
            try {
                const receipt = await create();
                const receivedAt = receipt.createdAt || new Date().toISOString();
                task = {
                    type: context.kind,
                    prompt: context.prompt,
                    model: context.model,
                    inputJson: context.inputJson,
                    attempts: 0,
                    ...receipt,
                    createdAt: receivedAt,
                    updatedAt: ("updatedAt" in receipt && receipt.updatedAt) || receivedAt,
                };
                // Creation is the acceptance boundary; detail reads cannot undo it.
                publish(task, true);
            } finally {
                gate.release(token);
            }
            return { status: "accepted" as const, ...await refresh(task, query, publish) };
        },
    };
}
