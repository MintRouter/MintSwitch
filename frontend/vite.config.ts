import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import wails from "@wailsio/runtime/plugins/vite";

// https://vitejs.dev/config/
export default defineConfig({
  server: {
    // Bind loopback only — never all interfaces (the dev UI manages API
    // keys). 127.0.0.1 is pinned explicitly because plain "localhost" can
    // resolve to [::1] (IPv6) while the Wails asset proxy dials tcp4
    // 127.0.0.1, which would leave the app window blank.
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  plugins: [svelte(), wails("./bindings")],
});
