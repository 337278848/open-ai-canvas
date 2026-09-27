import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { bootstrapAppearance } from "@/services/appearance-bootstrap";
import { getWelcomeAvailability } from "@/services/api/welcome";
import WelcomePage from "@/pages/welcome";
import { commitPublicAppearance, DEFAULT_PUBLIC_APPEARANCE } from "@/stores/use-appearance-store";

async function renderWelcome() {
    try {
        const appearanceReady = bootstrapAppearance().catch((error) => {
            console.error("Welcome appearance initialization failed", error);
            return commitPublicAppearance(DEFAULT_PUBLIC_APPEARANCE);
        });
        const availabilityReady = getWelcomeAvailability();
        const { welcomeEnabled } = await availabilityReady;
        if (welcomeEnabled !== true) {
            window.location.replace("/");
            return;
        }
        await appearanceReady;
        createRoot(document.getElementById("root")!).render(<StrictMode><WelcomePage /></StrictMode>);
    } catch (error) {
        console.error("Welcome page initialization failed", error);
        createRoot(document.getElementById("root")!).render(
            <main role="alert">
                <p>暂时无法打开欢迎页，请稍后重试。</p>
                <button onClick={() => window.location.reload()}>重试</button>
                <a href="/">返回首页</a>
            </main>,
        );
    }
}

void renderWelcome();
