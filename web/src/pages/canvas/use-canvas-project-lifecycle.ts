import { mergeAgentCanvasEditor } from "@/lib/canvas/agent-canvas-patch";
import { useCallback, useEffect, useLayoutEffect, useRef, useState, type Dispatch, type MutableRefObject, type SetStateAction } from "react";
import { App } from "antd";
import { useNavigate } from "react-router";

import { canvasAppearanceBaseTheme, canvasAppearanceForTheme, DEFAULT_CANVAS_BACKGROUND_MODE, normalizeCanvasAppearance, type CanvasAppearance } from "@/lib/canvas/canvas-appearance";
import type { CanvasBackgroundMode } from "@/lib/canvas-theme";
import { removeCanvasDrawing } from "@/lib/canvas/canvas-drawing-storage";
import { normalizeCanvasNodeTimestamps } from "@/lib/canvas/canvas-node-timestamps";
import { hydrateAssistantImages, resetInterruptedGeneration } from "@/lib/canvas/canvas-project-generation";
import { listAddedSkills, type Skill } from "@/services/api/skills";
import { createCanvasProjectWithRemoteSync, deleteCanvasProjectsWithRemoteSync, loadCanvasProjectForEditing, saveRemoteUserDataNow, subscribeAgentCanvasRefresh, restoreRemoteCanvasProject, type RestoreRemoteCanvasProjectInput } from "@/services/user-data-sync";
import { flushCanvasStorePersistence, useCanvasStore, type CanvasProject } from "@/stores/canvas/use-canvas-store";
import { useCanvasThemeStore } from "@/stores/canvas/use-canvas-theme-store";
import { useSyncProgressStore } from "@/stores/use-sync-progress-store";
import { useUserStore } from "@/stores/use-user-store";
import type { CanvasAssistantSession, CanvasConnection, CanvasNodeData, ViewportTransform } from "@/types/canvas";
import { getActiveUserScope } from "@/lib/user-scope";
import { readCanvasSyncDraftInventory } from "@/services/canvas-sync-drafts";
import type { CanvasHistorySnapshot } from "./use-canvas-history";

type UseCanvasProjectLifecycleOptions = {
    projectId: string;
    projectLoaded: boolean;
    nodes: CanvasNodeData[];
    connections: CanvasConnection[];
    chatSessions: CanvasAssistantSession[];
    activeChatId: string | null;
    canvasAppearance: CanvasAppearance;
    backgroundMode: CanvasBackgroundMode;
    showImageInfo: boolean;
    viewport: ViewportTransform;
    nodesRef: MutableRefObject<CanvasNodeData[]>;
    connectionsRef: MutableRefObject<CanvasConnection[]>;
    chatSessionsRef: MutableRefObject<CanvasAssistantSession[]>;
    activeChatIdRef: MutableRefObject<string | null>;
    viewportRef: MutableRefObject<ViewportTransform>;
    historyPausedRef: MutableRefObject<boolean>;
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
    setConnections: Dispatch<SetStateAction<CanvasConnection[]>>;
    setChatSessions: Dispatch<SetStateAction<CanvasAssistantSession[]>>;
    setActiveChatId: Dispatch<SetStateAction<string | null>>;
    setCanvasAppearance: Dispatch<SetStateAction<CanvasAppearance>>;
    setBackgroundMode: Dispatch<SetStateAction<CanvasBackgroundMode>>;
    setShowImageInfo: Dispatch<SetStateAction<boolean>>;
    setViewport: Dispatch<SetStateAction<ViewportTransform>>;
    setProjectLoaded: Dispatch<SetStateAction<boolean>>;
    resetHistory: (snapshot: CanvasHistorySnapshot) => void;
    cleanupAssetImages: (options?: unknown) => void;
    cleanupCanvasFiles: (extra?: unknown) => void;
};

export function useCanvasProjectLifecycle({
    projectId,
    projectLoaded,
    nodes,
    connections,
    chatSessions,
    activeChatId,
    canvasAppearance,
    backgroundMode,
    showImageInfo,
    viewport,
    nodesRef,
    connectionsRef,
    chatSessionsRef,
    activeChatIdRef,
    viewportRef,
    historyPausedRef,
    setNodes,
    setConnections,
    setChatSessions,
    setActiveChatId,
    setCanvasAppearance,
    setBackgroundMode,
    setShowImageInfo,
    setViewport,
    setProjectLoaded,
    resetHistory,
    cleanupAssetImages,
    cleanupCanvasFiles,
}: UseCanvasProjectLifecycleOptions) {
    const { message } = App.useApp();
    const navigate = useNavigate();
    const hydrated = useCanvasStore((state) => state.hydrated);
    const sessionHydrated = useUserStore((state) => state.hydrated);
    const openProject = useCanvasStore((state) => state.openProject);
    const updateProject = useCanvasStore((state) => state.updateProject);
    const renameProject = useCanvasStore((state) => state.renameProject);
    const currentProject = useCanvasStore((state) => state.projects.find((project) => project.id === projectId));
    const [addedSkills, setAddedSkills] = useState<Skill[]>([]);
    const [loadError, setLoadError] = useState("");
    const [loadAttempt, setLoadAttempt] = useState(0);
    const [agentCreatedNodes, setAgentCreatedNodes] = useState<{ projectId: string; nodes: CanvasNodeData[] } | null>(null);
    const viewportSaveTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

    const observedContentRef = useRef<CanvasHistorySnapshot | null>(null);
    const editorReadyRef = useRef(false);
    const scope = getActiveUserScope();
    const contextRef = useRef({ projectId, scope });
    if (contextRef.current.projectId !== projectId || contextRef.current.scope !== scope) contextRef.current = { projectId, scope };
    const context = contextRef.current;
    const mountedRef = useRef(false);
    const loadEpochRef = useRef(0);
    const manualRestoreRef = useRef(false);
    const restoredProjectionRef = useRef<CanvasHistorySnapshot | null>(null);
    const [manualRestoring, setManualRestoring] = useState(false);
    const isCurrentContext = useCallback(() => mountedRef.current && contextRef.current === context && getActiveUserScope() === scope, [context, scope]);
    useEffect(() => { mountedRef.current = true; return () => { mountedRef.current = false; }; }, []);
    useLayoutEffect(() => {
        // A stale restore must not leave the next canvas/account locked.
        ++loadEpochRef.current;
        manualRestoreRef.current = false;
        restoredProjectionRef.current = null;
        editorReadyRef.current = false;
        observedContentRef.current = null;
        setManualRestoring(false);
        setProjectLoaded(false);
    }, [context, setProjectLoaded]);
    const applyProjectToEditor = useCallback((targetProject: CanvasProject) => {
            if (!isCurrentContext() || targetProject.id !== projectId) return;
            const fallbackTheme = useCanvasThemeStore.getState().theme;
            const restoredAppearance = targetProject.appearance
                ? normalizeCanvasAppearance(targetProject.appearance, fallbackTheme)
                : canvasAppearanceForTheme(fallbackTheme);
            const initialNodes = normalizeCanvasNodeTimestamps(resetInterruptedGeneration(targetProject.nodes), {
                createdAt: targetProject.createdAt,
                updatedAt: targetProject.updatedAt,
            });
            const snapshot: CanvasHistorySnapshot = {
                nodes: initialNodes,
                connections: targetProject.connections,
                chatSessions: targetProject.chatSessions || [],
                activeChatId: targetProject.activeChatId || null,
                canvasAppearance: restoredAppearance,
                backgroundMode: targetProject.backgroundMode || DEFAULT_CANVAS_BACKGROUND_MODE,
                showImageInfo: targetProject.showImageInfo || false,
            };
            observedContentRef.current = snapshot;
            chatSessionsRef.current = snapshot.chatSessions;
            activeChatIdRef.current = snapshot.activeChatId;
            nodesRef.current = snapshot.nodes;
            connectionsRef.current = snapshot.connections;
            viewportRef.current = targetProject.viewport;
            setNodes(snapshot.nodes);
            setConnections(snapshot.connections);
            setChatSessions(snapshot.chatSessions);
            setActiveChatId(snapshot.activeChatId);
            setCanvasAppearance(snapshot.canvasAppearance);
            useCanvasThemeStore.getState().setTheme(canvasAppearanceBaseTheme(snapshot.canvasAppearance, fallbackTheme));
            setBackgroundMode(snapshot.backgroundMode);
            setShowImageInfo(snapshot.showImageInfo);
            setViewport(targetProject.viewport);
            resetHistory(snapshot);
            editorReadyRef.current = true;
            setProjectLoaded(true);
        }, [isCurrentContext, projectId, resetHistory, setNodes, setConnections, setChatSessions, setActiveChatId, setCanvasAppearance, setBackgroundMode, setShowImageInfo, setViewport, setProjectLoaded, nodesRef, connectionsRef, chatSessionsRef, activeChatIdRef, viewportRef]);

    useEffect(() => {
        if (!hydrated || !sessionHydrated) return;
        let cancelled = false;
        const loadEpoch = ++loadEpochRef.current;
        // Keep load intent on the refs until this attempt finishes. React Strict
        // Mode remounts the effect; consuming the flags here would turn "load
        // latest" into a normal open and immediately recreate the conflict.
        const keepEditor = editorReadyRef.current;
        if (!keepEditor) {
            editorReadyRef.current = false;
            setProjectLoaded(false);
            setLoadError("");
            observedContentRef.current = null;
        }
        const applyRestoredProject = (targetProject: CanvasProject) => {
            if (cancelled || loadEpochRef.current !== loadEpoch || manualRestoreRef.current || !isCurrentContext()) return;
            applyProjectToEditor(targetProject);
        };

        const load = async () => {
            const cachedProject = useCanvasStore.getState().projects.find((p) => p.id === projectId);
            if (cachedProject && cachedProject.nodes?.length) {
                // 本地已有该画布的持久化缓存：先以本地数据秒开渲染，彻底消除白屏与等待
                applyRestoredProject(cachedProject);
            }
            const loadedProject = await loadCanvasProjectForEditing(projectId, { onLoad: applyRestoredProject });
            if (cancelled || loadEpochRef.current !== loadEpoch || manualRestoreRef.current || !isCurrentContext()) return;
            if (!loadedProject) {
                if (!cachedProject) navigate("/canvas", { replace: true });
                return;
            }
            const project = useCanvasStore.getState().projects.find((p) => p.id === projectId) || loadedProject;

            // 画布媒体由节点自己的视口观察器按需加载；打开时遍历并解析全部节点会让大画布形成 N+1 资源读取。
            void hydrateAssistantImages(project.chatSessions || [])
                .then((hydratedSessions) => {
                    if (!cancelled && loadEpochRef.current === loadEpoch && !manualRestoreRef.current && isCurrentContext()) setChatSessions((current) => {
                        const merged = mergeHydratedSessions(current, hydratedSessions);
                        if (observedContentRef.current?.chatSessions === current) observedContentRef.current = { ...observedContentRef.current, chatSessions: merged };
                        return merged;
                    });
                })
                .catch(() => {
                    if (!cancelled && loadEpochRef.current === loadEpoch && !manualRestoreRef.current && isCurrentContext()) message.warning("部分助手会话素材恢复失败，已使用项目记录继续打开");
                });
        };
        void load()
            .then(() => {
                if (cancelled || loadEpochRef.current !== loadEpoch || manualRestoreRef.current || !isCurrentContext()) return;
            })
            .catch((error) => {
                if (cancelled || loadEpochRef.current !== loadEpoch || manualRestoreRef.current || !isCurrentContext()) return;
                useSyncProgressStore.getState().setProjectProgress(projectId, { phase: "error", message: error instanceof Error ? error.message : "读取云端版本失败" });
                const detail = error instanceof Error ? error.message : "读取画布失败，请重试";
                if (keepEditor) message.error(detail);
                else setLoadError(detail);
            });
        return () => {
            cancelled = true;
        };
    }, [applyProjectToEditor, isCurrentContext, scope, hydrated, sessionHydrated, loadAttempt, message, navigate, openProject, projectId, resetHistory, setActiveChatId, setBackgroundMode, setCanvasAppearance, setChatSessions, setConnections, setNodes, setShowImageInfo, setViewport]);

    useEffect(() => {
        if (!projectLoaded) return;
        let cancelled = false;
        listAddedSkills()
            .then(({ skills }) => {
                if (!cancelled) setAddedSkills(skills);
            })
            .catch(() => {
                if (!cancelled) setAddedSkills([]);
            });
        return () => {
            cancelled = true;
        };
    }, [projectLoaded]);

    useEffect(() => subscribeAgentCanvasRefresh((project, previous) => {
        if (!isCurrentContext() || manualRestoreRef.current || !projectLoaded || project.id !== projectId) return;
        // Merge only server-changed fields so dragging/editing other nodes can
        // continue while Agent media tasks complete. Same-field conflicts fail.
        const merged = previous ? mergeAgentCanvasEditor(previous, project, nodesRef.current, connectionsRef.current) : project;
        if (observedContentRef.current) {
            const observed = observedContentRef.current;
            // Advance only the observed server fields; edits in live refs still
            // differ from this baseline and must be persisted by the effect below.
            const baseline = previous ? mergeAgentCanvasEditor(previous, project, observed.nodes, observed.connections) : project;
            observedContentRef.current = { ...observed, nodes: baseline.nodes, connections: baseline.connections };
        }
        nodesRef.current = merged.nodes;
        connectionsRef.current = merged.connections;
        setNodes(merged.nodes);
        setConnections(merged.connections);
        const previousIds = new Set(previous?.nodes.map((node) => node.id));
        const created = project.nodes.filter((node) => !previousIds.has(node.id));
        if (created.length) setAgentCreatedNodes({ projectId: project.id, nodes: created });
    }), [isCurrentContext, projectId, projectLoaded, nodesRef, connectionsRef, setNodes, setConnections]);

    useEffect(() => {
        if (!isCurrentContext() || !editorReadyRef.current || manualRestoreRef.current || !projectLoaded || historyPausedRef.current || nodes !== nodesRef.current || connections !== connectionsRef.current) return;
        const snapshot = { nodes, connections, chatSessions, activeChatId, canvasAppearance, backgroundMode, showImageInfo };
        if (!observedContentRef.current || sameCanvasHistorySnapshot(observedContentRef.current, snapshot)) return;
        observedContentRef.current = snapshot;
        const patch = { nodes, connections, chatSessions, activeChatId, appearance: canvasAppearance, backgroundMode, showImageInfo };
        const stored = useCanvasStore.getState().projects.find((project) => project.id === projectId);
        // 远端结果投影到编辑器不是一次本地编辑，避免改写时间戳并触发反向保存。
        if (stored && sameCanvasPatch(stored, patch)) return;
        updateProject(projectId, patch);
    }, [isCurrentContext, activeChatId, backgroundMode, canvasAppearance, chatSessions, connections, historyPausedRef, nodes, projectId, projectLoaded, showImageInfo, updateProject]);

    useEffect(() => {
        if (!isCurrentContext() || manualRestoreRef.current || !projectLoaded) return;
        if (viewportSaveTimerRef.current) clearTimeout(viewportSaveTimerRef.current);
        viewportSaveTimerRef.current = setTimeout(() => {
            if (!isCurrentContext() || manualRestoreRef.current) return;
            updateProject(projectId, { viewport: viewportRef.current });
            viewportSaveTimerRef.current = null;
        }, 500);
        return () => {
            if (viewportSaveTimerRef.current) clearTimeout(viewportSaveTimerRef.current);
        };
    }, [isCurrentContext, projectId, projectLoaded, updateProject, viewport, viewportRef]);

    useEffect(() => () => {
        if (!isCurrentContext() || manualRestoreRef.current || !projectLoaded) return;
        if (viewportSaveTimerRef.current) clearTimeout(viewportSaveTimerRef.current);
        updateProject(projectId, { viewport: viewportRef.current });
    }, [isCurrentContext, projectId, projectLoaded, updateProject, viewportRef]);

    const createAndOpenProject = useCallback(() => {
        void createCanvasProjectWithRemoteSync(`自由画布 ${useCanvasStore.getState().projects.length + 1}`).then(({ id, syncError }) => {
            if (syncError) message.warning(syncError instanceof Error ? `画布已在本地创建，云端同步失败：${syncError.message}` : "画布已在本地创建，云端同步失败");
            navigate(`/canvas/${id}`);
        });
    }, [message, navigate]);

    const deleteCurrentProject = useCallback(async () => {
        if (!isCurrentContext() || manualRestoreRef.current) return;
        const drawingIds = nodesRef.current.flatMap((node) => node.type === "drawing" && node.metadata?.drawingId ? [node.metadata.drawingId] : []);
        let retainedDrawings: Set<string> | null = null;
        let retainedDrafts: CanvasProject[] | null = null;
        try {
            const inventory = await readCanvasSyncDraftInventory(projectId, scope);
            if (!inventory.issues.length) {
                retainedDrafts = inventory.drafts.map((draft) => draft.project);
                retainedDrawings = new Set(retainedDrafts.flatMap((draft) => draft.nodes.flatMap((node) => node.type === "drawing" && node.metadata?.drawingId ? [node.metadata.drawingId] : [])));
            }
        } catch { /* Keep drawing caches if recovery references cannot be verified. */ }
        if (!isCurrentContext() || manualRestoreRef.current) return;
        try {
            await deleteCanvasProjectsWithRemoteSync([projectId]);
        } catch (error) {
            if (!isCurrentContext()) return;
            message.error(error instanceof Error ? `删除画布失败：${error.message}` : "删除画布失败，请稍后重试");
            return;
        }
        if (!isCurrentContext()) return;
        const removableDrawings = retainedDrawings ? drawingIds.filter((id) => !retainedDrawings.has(id)) : [];
        if (removableDrawings.length) {
            void Promise.all(removableDrawings.map((drawingId) => removeCanvasDrawing(projectId, drawingId)))
                .catch(() => { if (isCurrentContext()) message.warning("项目已删除，但部分本地绘图缓存清理失败"); });
        }
        // Recovery references are not live projects; do not collect their media.
        if (retainedDrafts) cleanupAssetImages({ drafts: retainedDrafts });
        navigate("/canvas");
    }, [isCurrentContext, scope, cleanupAssetImages, message, navigate, nodesRef, projectId]);

    const renameCurrentProject = useCallback((title: string) => {
        renameProject(projectId, title);
        // 标题是画布列表和分享入口的元数据，重命名后立即提交，避免只停留在浏览器缓存。
        void saveRemoteUserDataNow(projectId).catch((error) => {
            // 后台同步状态统一显示在同步面板，避免每次重试都打断编辑。
            void error;
        });
    }, [message, projectId, renameProject]);

    const persistLocalEdits = useCallback(async (options: { flush?: boolean } = {}) => {
        if (!isCurrentContext()) throw new Error("画布或账号已切换，已停止保存");
        const snapshot = { nodes: nodesRef.current, connections: connectionsRef.current, chatSessions, activeChatId, canvasAppearance, backgroundMode, showImageInfo };
        if (observedContentRef.current && !sameCanvasHistorySnapshot(observedContentRef.current, snapshot)) {
            updateProject(projectId, {
                nodes: nodesRef.current,
                connections: connectionsRef.current,
                chatSessions,
                activeChatId,
                appearance: canvasAppearance,
                backgroundMode,
                showImageInfo,
                viewport: viewportRef.current,
            });
            observedContentRef.current = snapshot;
        }
        updateProject(projectId, { viewport: viewportRef.current });
        if (options.flush !== false) await flushCanvasStorePersistence();
        if (!isCurrentContext()) throw new Error("画布或账号已切换，已停止保存");
    }, [isCurrentContext, activeChatId, backgroundMode, canvasAppearance, chatSessions, connectionsRef, nodesRef, projectId, showImageInfo, updateProject, viewportRef]);

    useLayoutEffect(() => {
        // Keep old autosave effects blocked until React commits restored content.
        const restored = restoredProjectionRef.current;
        const visible = { nodes, connections, chatSessions, activeChatId, canvasAppearance, backgroundMode, showImageInfo };
        if (!manualRestoring || !restored || !isCurrentContext() || !sameCanvasHistorySnapshot(restored, visible)) return;
        restoredProjectionRef.current = null;
        manualRestoreRef.current = false;
        setManualRestoring(false);
    }, [manualRestoring, isCurrentContext, nodes, connections, chatSessions, activeChatId, canvasAppearance, backgroundMode, showImageInfo]);

    const restoreCanvasVersion = useCallback(async (input: RestoreRemoteCanvasProjectInput) => {
        if (!isCurrentContext() || input.projectId !== projectId || input.expectedScope !== scope || !editorReadyRef.current || manualRestoreRef.current) throw new Error("画布或账号已切换，或正在恢复，请重新打开版本记录");
        manualRestoreRef.current = true;
        setManualRestoring(true);
        ++loadEpochRef.current;
        if (viewportSaveTimerRef.current) clearTimeout(viewportSaveTimerRef.current);
        try {
            // Flush live editor refs before the helper snapshots the current draft.
            await persistLocalEdits();
            if (!isCurrentContext()) throw new Error("画布或账号已切换，已停止恢复");
            const restored = await restoreRemoteCanvasProject(input);
            if (!isCurrentContext()) throw new Error("画布或账号已切换，已停止恢复结果投影");
            applyProjectToEditor(restored);
            restoredProjectionRef.current = observedContentRef.current;
        } catch (error) {
            if (isCurrentContext()) {
                restoredProjectionRef.current = null;
                manualRestoreRef.current = false;
                setManualRestoring(false);
            }
            throw error;
        }
    }, [isCurrentContext, projectId, scope, persistLocalEdits, applyProjectToEditor]);

    const saveCanvasProject = useCallback(async (options: { requireRemote?: boolean } = {}): Promise<boolean> => {
        if (!isCurrentContext() || manualRestoreRef.current) return false;
        try {
            await persistLocalEdits();
        } catch {
            message.error("画布保存失败，请稍后重试");
            return false;
        }
        try {
            await saveRemoteUserDataNow(projectId);
            message.success("画布已保存到云端");
        } catch (error) {
            const detail = error instanceof Error ? error.message : "未知错误";
            // 本地保存已经完成，云端状态统一显示在同步面板，不弹出同步失败 toast。
            void detail;
            // Imports can retain their durable local result; sharing requires cloud success.
            return options.requireRemote === false;
        }
        return true;
    }, [isCurrentContext, message, persistLocalEdits, projectId]);

    const clearCanvasFiles = useCallback(() => {
        cleanupCanvasFiles({ projectId, nodes: [], chatSessions: [] });
    }, [cleanupCanvasFiles, projectId]);

    return {
        loadError,
        manualRestoring,
        restoreCanvasVersion,
        retryLoad: () => setLoadAttempt((attempt) => attempt + 1),
        addedSkills,
        agentCreatedNodes: agentCreatedNodes?.projectId === projectId ? agentCreatedNodes.nodes : null,
        clearCanvasFiles,
        createAndOpenProject,
        currentProject,
        deleteCurrentProject,
        renameCurrentProject,
        saveCanvasProject,
        updateProject,
    };
}


function sameCanvasHistorySnapshot(left: CanvasHistorySnapshot, right: CanvasHistorySnapshot) {
    return left.nodes === right.nodes
        && left.connections === right.connections
        && left.chatSessions === right.chatSessions
        && left.activeChatId === right.activeChatId
        && left.canvasAppearance === right.canvasAppearance
        && left.backgroundMode === right.backgroundMode
        && left.showImageInfo === right.showImageInfo;
}

function sameCanvasPatch(project: CanvasProject, patch: Pick<CanvasProject, "nodes" | "connections" | "chatSessions" | "activeChatId" | "backgroundMode" | "showImageInfo"> & { appearance: CanvasProject["appearance"] }) {
    return project.nodes === patch.nodes
        && project.connections === patch.connections
        && project.chatSessions === patch.chatSessions
        && project.activeChatId === patch.activeChatId
        && project.appearance === patch.appearance
        && project.backgroundMode === patch.backgroundMode
        && project.showImageInfo === patch.showImageInfo;
}

function mergeHydratedSessions(currentSessions: CanvasAssistantSession[], hydratedSessions: CanvasAssistantSession[]) {
    const hydratedById = new Map(hydratedSessions.map((session) => [session.id, session]));
    return currentSessions.map((session) => {
        const hydrated = hydratedById.get(session.id);
        if (!hydrated) return session;
        const hydratedMessages = new Map(hydrated.messages.map((message) => [message.id, message]));
        return {
            ...session,
            messages: session.messages.map((message) => {
                const hydratedMessage = hydratedMessages.get(message.id);
                if (!hydratedMessage || !message.references?.length) return message;
                const hydratedReferences = new Map((hydratedMessage.references || []).map((reference) => [reference.id, reference]));
                return {
                    ...message,
                    references: message.references.map((reference) => {
                        const hydratedReference = hydratedReferences.get(reference.id);
                        return hydratedReference ? { ...reference, dataUrl: hydratedReference.dataUrl, storageKey: hydratedReference.storageKey } : reference;
                    }),
                };
            }),
        };
    });
}
