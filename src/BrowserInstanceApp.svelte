<script lang="ts">
  import { invoke } from "@tauri-apps/api/core";
  import { listen } from "@tauri-apps/api/event";
  import { onMount } from "svelte";
  import { BrowserIcon as Browser, WarningCircleIcon as WarningCircle } from "phosphor-svelte";

  type InstanceMetadata = {
    instance_id: string;
    account_name: string;
  };

  type BrowserWebviewEvent = {
    instance_id: string;
    message?: string;
  };

  const instanceId = new URL(window.location.href).searchParams.get("instance")?.trim() || "";
  const isWindowsDesktop = /Windows/i.test(navigator.userAgent);
  let viewport: HTMLElement;
  let metadata: InstanceMetadata | null = null;
  let loading = true;
  let error = "";
  let mounted = false;
  let resizeFrame = 0;

  function bounds() {
    const rect = viewport.getBoundingClientRect();
    return { x: rect.left, y: rect.top, width: rect.width, height: rect.height };
  }

  async function syncBrowserSurface() {
    if (!instanceId || !viewport || !viewport.isConnected) return;
    window.cancelAnimationFrame(resizeFrame);
    resizeFrame = window.requestAnimationFrame(async () => {
      try {
        if (!mounted) {
          await invoke("mount_browser_webview", { instanceId, bounds: bounds() });
          mounted = true;
          return;
        }
        await invoke("sync_browser_webview", { instanceId, bounds: bounds(), reveal: !loading && !error });
      } catch (reason) {
        error = reason instanceof Error ? reason.message : String(reason);
        loading = false;
      }
    });
  }

  onMount(() => {
    if (!instanceId) {
      error = "浏览器实例标识缺失";
      loading = false;
      return;
    }
    void invoke<InstanceMetadata>("browser_instance_window_metadata", { instanceId })
      .then((result) => (metadata = result))
      .catch((reason) => {
        error = reason instanceof Error ? reason.message : String(reason);
        loading = false;
      });

    const observer = new ResizeObserver(() => void syncBrowserSurface());
    observer.observe(viewport);
    void syncBrowserSurface();
    let unlistenReady: (() => void) | undefined;
    let unlistenError: (() => void) | undefined;
    void listen<BrowserWebviewEvent>("browser-webview://ready", (event) => {
      if (event.payload.instance_id !== instanceId) return;
      loading = false;
      void syncBrowserSurface();
    }).then((unlisten) => (unlistenReady = unlisten));
    void listen<BrowserWebviewEvent>("browser-webview://load-error", (event) => {
      if (event.payload.instance_id !== instanceId) return;
      error = event.payload.message || "实例页面加载失败";
      loading = false;
    }).then((unlisten) => (unlistenError = unlisten));
    const cookieTimer = window.setInterval(() => {
      void invoke("sync_browser_account_cookie", { instanceId }).catch(() => undefined);
    }, 60_000);

    return () => {
      observer.disconnect();
      window.cancelAnimationFrame(resizeFrame);
      window.clearInterval(cookieTimer);
      unlistenReady?.();
      unlistenError?.();
    };
  });
</script>

<main class="instance-window-shell">
  {#if !isWindowsDesktop}
    <header data-tauri-drag-region>
      <div data-tauri-drag-region>
        <span><Browser size={14} /></span>
        <strong>{metadata?.account_name || "浏览器实例"}</strong>
      </div>
    </header>
  {/if}
  <section bind:this={viewport} class="instance-window-viewport">
    {#if loading && !error}<div class="instance-window-state"><i></i><span>正在打开实例…</span></div>{/if}
    {#if error}<div class="instance-window-state error"><WarningCircle size={20} /><span>{error}</span></div>{/if}
  </section>
</main>

<style>
  :global(html), :global(body), :global(#app) { width: 100%; height: 100%; margin: 0; overflow: hidden; background: #fff; }
  .instance-window-shell { display: flex; width: 100%; height: 100%; flex-direction: column; background: #fff; }
  header { display: flex; min-height: 40px; align-items: center; justify-content: center; border-bottom: 1px solid #ece8e1; user-select: none; }
  header > div { display: flex; align-items: center; gap: 7px; }
  header span { display: grid; width: 22px; height: 22px; place-items: center; border-radius: 6px; background: #edf2fd; color: #5879d8; }
  header strong { color: #403c36; font-size: 11px; font-weight: 650; }
  .instance-window-viewport { position: relative; min-height: 0; flex: 1 1 auto; overflow: hidden; background: #f7f6f3; }
  .instance-window-state { position: absolute; inset: 0; display: grid; place-content: center; justify-items: center; gap: 8px; color: #8b867e; font-size: 11px; }
  .instance-window-state i { width: 17px; height: 17px; border: 2px solid #d8d4cc; border-top-color: #5879d8; border-radius: 50%; animation: spin .8s linear infinite; }
  .instance-window-state.error { color: #b45444; }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
