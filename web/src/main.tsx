import "@fontsource-variable/inter";
import "@fontsource-variable/jetbrains-mono";
import { bootstrapAppearance } from "@/services/appearance-bootstrap";

// The public film entry checks its availability independently of workspace bootstrap.
if (/^\/welcome\/?$/.test(window.location.pathname)) void import("./welcome-application");
else {
    // 外观配置只是增强信息，不能阻塞主应用启动。
    // 如果该接口被浏览器拦截、后端暂时无响应或网络异常，页面仍应先挂载，
    // 否则用户会永久停留在 index.html 的“正在加载”占位页。
    void import("./application").catch((error) => {
        console.error("[canvas] application bootstrap failed", error);
        const root = document.getElementById("root");
        if (root) root.innerHTML = '<div style="padding:24px;color:#fff;font-family:system-ui">页面启动失败，请刷新后重试。</div>';
    });
    void bootstrapAppearance();
}
