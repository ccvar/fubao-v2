<script lang="ts">
  import { invoke } from "@tauri-apps/api/core";
  import { onMount } from "svelte";
  import {
    ArrowSquareOutIcon as ArrowSquareOut,
    BrowserIcon as Browser,
    WarningCircleIcon as WarningCircle,
  } from "phosphor-svelte";

  type InstanceMetadata = {
    instance_id: string;
    account_id?: string;
    account_name: string;
    cookie_status?: string;
    surface?: string;
  };

  type ExternalInstance = {
    id: string;
    status?: "online" | "stopped";
    account_name?: string;
  };

  const instanceId = new URL(window.location.href).searchParams.get("instance")?.trim() || "";
  const isWindowsDesktop = /Windows/i.test(navigator.userAgent);

  let metadata: InstanceMetadata | null = null;
  let status: "starting" | "online" | "stopped" | "error" = "starting";
  let message = "正在打开 Chrome 并恢复登录…";
  let error = "";
  let launching = false;

  async function launchChrome(focusOnly = false) {
    if (!instanceId || launching) return;
    launching = true;
    // Keep the previous success UI while refocusing so a transient RPC blip
    // does not flash “无法打开 Chrome” over a healthy companion window.
    if (!focusOnly || status !== "online") {
      status = "starting";
      message = focusOnly ? "正在显示 Chrome 窗口…" : "正在打开 Chrome 并恢复登录…";
      error = "";
    }
    try {
      await invoke<ExternalInstance>("launch_external_chrome_instance", {
        instanceId,
      });
      status = "online";
      message = "抖音已在配套 Chrome 窗口中运行。关闭本壳不会退出 Chrome。";
      error = "";
    } catch (reason) {
      const text = reason instanceof Error ? reason.message : String(reason);
      // First launch can race the debugger port; one quiet retry covers cold starts.
      if (!focusOnly) {
        try {
          await new Promise((resolve) => window.setTimeout(resolve, 900));
          await invoke<ExternalInstance>("launch_external_chrome_instance", {
            instanceId,
          });
          status = "online";
          message = "抖音已在配套 Chrome 窗口中运行。关闭本壳不会退出 Chrome。";
          error = "";
          return;
        } catch (retryReason) {
          const retryText = retryReason instanceof Error ? retryReason.message : String(retryReason);
          // Engine timeout while Chrome may already be open — do not look like a hard failure.
          if (/超时|timeout/i.test(text) || /超时|timeout/i.test(retryText)) {
            status = "online";
            message = "Chrome 可能已打开；若未见窗口请点下方「显示 Chrome 窗口」。关闭本壳不会退出 Chrome。";
            error = "";
            return;
          }
        }
      }
      status = "error";
      error = text;
      message = text;
    } finally {
      launching = false;
    }
  }

  onMount(() => {
    if (!instanceId) {
      status = "error";
      error = "浏览器实例标识缺失";
      message = error;
      return;
    }
    void invoke<InstanceMetadata>("browser_instance_window_metadata", { instanceId })
      .then((result) => {
        metadata = result;
        document.title = `福宝浏览器实例 · ${result.account_name || "导入账号"}`;
      })
      .catch(() => {
        // Title metadata is optional; launching Chrome is the critical path.
      });
    void launchChrome(false);
  });
</script>

<main class="instance-window-shell external-shell">
  {#if !isWindowsDesktop}
    <header data-tauri-drag-region>
      <div data-tauri-drag-region>
        <span><Browser size={12} /></span>
        <strong>{metadata?.account_name || "浏览器实例"}</strong>
        <em>Chrome 外窗</em>
      </div>
    </header>
  {/if}
  <section class="external-shell-body">
    {#if status === "starting" || launching}
      <div class="instance-window-state">
        <i></i>
        <span>{message}</span>
      </div>
    {:else if status === "error"}
      <div class="instance-window-state error">
        <WarningCircle size={22} />
        <strong>无法打开 Chrome</strong>
        <span>{error || message}</span>
        <button type="button" class="shell-action" onclick={() => launchChrome(false)} disabled={launching}>
          重试打开
        </button>
      </div>
    {:else}
      <div class="instance-window-state ready">
        <span class="ready-icon"><Browser size={22} /></span>
        <strong>{metadata?.account_name || "导入账号"}</strong>
        <span>{message}</span>
        <div class="shell-actions">
          <button type="button" class="shell-action primary" onclick={() => launchChrome(true)} disabled={launching}>
            <ArrowSquareOut size={14} />
            显示 Chrome 窗口
          </button>
        </div>
        <small>导入 Cookie 使用系统 Chrome 保持登录；扫码/重绑账号仍为卡片内嵌。</small>
      </div>
    {/if}
  </section>
</main>

<style>
  :global(html),
  :global(body),
  :global(#app) {
    width: 100%;
    height: 100%;
    margin: 0;
    overflow: hidden;
    background: #fff;
  }
  .instance-window-shell {
    display: flex;
    width: 100%;
    height: 100%;
    flex-direction: column;
    background: #fff;
  }
  header {
    display: flex;
    height: 28px;
    min-height: 28px;
    max-height: 28px;
    align-items: center;
    justify-content: center;
    border-bottom: 1px solid #ece8e1;
    user-select: none;
  }
  header > div {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  header span {
    display: grid;
    width: 18px;
    height: 18px;
    place-items: center;
    border-radius: 5px;
    background: #edf2fd;
    color: #5879d8;
  }
  header strong {
    color: #403c36;
    font-size: 11px;
    font-weight: 650;
  }
  header em {
    color: #8b867e;
    font-size: 10px;
    font-style: normal;
    font-weight: 550;
  }
  .external-shell-body {
    position: relative;
    min-height: 0;
    flex: 1 1 auto;
    overflow: hidden;
    background: linear-gradient(180deg, #fbfaf8 0%, #f4f2ed 100%);
  }
  .instance-window-state {
    position: absolute;
    inset: 0;
    display: grid;
    place-content: center;
    justify-items: center;
    gap: 8px;
    padding: 24px;
    color: #8b867e;
    font-size: 11px;
    text-align: center;
  }
  .instance-window-state strong {
    color: #403c36;
    font-size: 13px;
    font-weight: 650;
  }
  .instance-window-state i {
    width: 17px;
    height: 17px;
    border: 2px solid #d8d4cc;
    border-top-color: #5879d8;
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  .instance-window-state.error {
    color: #b45444;
  }
  .instance-window-state.ready .ready-icon {
    display: grid;
    width: 42px;
    height: 42px;
    place-items: center;
    border-radius: 12px;
    background: #edf2fd;
    color: #5879d8;
  }
  .instance-window-state small {
    max-width: 280px;
    color: #a39e96;
    line-height: 1.45;
  }
  .shell-actions {
    display: flex;
    gap: 8px;
    margin-top: 4px;
  }
  .shell-action {
    display: inline-flex;
    min-height: 30px;
    align-items: center;
    justify-content: center;
    gap: 6px;
    border: 1px solid #e5e0d8;
    border-radius: 8px;
    background: #fff;
    color: #403c36;
    padding: 0 12px;
    font-size: 11px;
    font-weight: 600;
    cursor: pointer;
  }
  .shell-action.primary {
    border-color: #c9d6f5;
    background: #edf2fd;
    color: #3f5fbf;
  }
  .shell-action:disabled {
    opacity: 0.55;
    cursor: default;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
