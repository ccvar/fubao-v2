import { mount } from "svelte";
import App from "./DesktopApp.svelte";
import MonitorLogApp from "./MonitorLogApp.svelte";
import "./styles.css";

function reportFrontendError(level: string, value: unknown) {
  const message =
    value instanceof Error
      ? `${value.name}: ${value.message}\n${value.stack ?? ""}`
      : String(value);
  if (level !== "info") {
    console.error(`[frontend:${level}] ${message}`);
  }
  if ("__TAURI_INTERNALS__" in window) {
    void import("@tauri-apps/api/core")
      .then(({ invoke }) => invoke("frontend_log", { level, message }))
      .catch(() => undefined);
  }
}

window.addEventListener("error", (event) => {
  reportFrontendError("error", event.error ?? event.message);
});

window.addEventListener("unhandledrejection", (event) => {
  reportFrontendError("rejection", event.reason);
});

reportFrontendError("info", `bootstrap ${window.location.href}`);

try {
  const target = document.getElementById("app");
  if (!target) throw new Error("找不到应用挂载节点");
  const isMonitorLogWindow = new URL(window.location.href).searchParams.get("window") === "monitor-log";
  mount(isMonitorLogWindow ? MonitorLogApp : App, { target });
  if (window.location.protocol === "http:" || window.location.protocol === "https:") {
    window.setTimeout(() => {
      void import("@tauri-apps/api/core")
        .then(({ invoke }) => invoke("refresh_window_surface"))
        .catch((error) => reportFrontendError("surface", error));
    }, 700);
  }
  window.setTimeout(() => {
    reportFrontendError(
      "info",
      `mounted children=${target.childElementCount} text=${target.textContent?.trim().length ?? 0}`,
    );
  }, 1000);
} catch (error) {
  reportFrontendError("mount", error);
  const target = document.getElementById("app");
  if (target) {
    target.innerHTML =
      '<div style="padding:72px 28px;color:#4b4740;font:14px -apple-system,sans-serif">客户端界面加载失败，请查看启动日志。</div>';
  }
}
