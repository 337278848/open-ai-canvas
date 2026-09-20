// Shared, in-context model boundaries. Uses workspace surface/text/status tokens
// so the same component works in creation, canvas and the production workbench.
import { CheckCircle2, CircleHelp, TriangleAlert } from "lucide-react";
import { assessModelApplicability } from "@/lib/model-applicability";
import type { ModelRequirements } from "@/lib/model-selection";
import type { AiConfig } from "@/stores/use-config-store";
import { cn } from "@/lib/utils";
import "./model-capability.css";

export function ModelCapabilityHint({ config, model, requirements, className }: { config: AiConfig; model: string; requirements?: ModelRequirements; className?: string }) {
    if (requirements?.capability !== "video") return null;
    const assessment = assessModelApplicability(config, model, requirements);
    const Icon = assessment.status === "ready" ? CheckCircle2 : assessment.status === "unknown" ? CircleHelp : TriangleAlert;
    const status = { ready: "当前条件适用", needs_input: "还需补充素材", incompatible: "当前条件不适用", unknown: "能力待确认" }[assessment.status];
    return <section className={cn("model-capability-hint", className)} data-state={assessment.status} aria-label="当前模型能力">
        <div className="model-capability-status" role="status" aria-live="polite">
            <Icon aria-hidden="true" />
            <strong>{status}</strong>
            {assessment.reason ? <span>{assessment.reason}</span> : null}
        </div>
        {assessment.summary.length ? <ul className="model-capability-facts">
            {assessment.summary.slice(0, 5).map((fact) => <li key={fact}>{fact}</li>)}
        </ul> : null}
        <details>
            <summary>完整能力与限制</summary>
            <ul className="model-capability-details">
                {assessment.summary.slice(5).map((fact) => <li key={fact}>{fact}</li>)}
            </ul>
            <p>按当前模型和渠道的能力配置匹配；提交前服务端会再次校验。不匹配时请调整条件或换模型，已选素材不会自动移除。</p>
        </details>
    </section>;
}
