import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { homedir } from "node:os";
import { join } from "node:path";

// 浏览器开发模式：/api/* 经 Vite 代理转发到 velumd 的 Unix socket（Tauri 中则走 Rust 命令）。
const velumHome = process.env.VELUM_HOME ?? join(homedir(), "Library/Application Support/Velum");

export default defineConfig({
  plugins: [react(), tailwindcss()],
  clearScreen: false,
  server: {
    port: 1420,
    strictPort: true,
    proxy: {
      "/api": {
        target: { socketPath: join(velumHome, "velumd.sock") } as never,
        rewrite: (p) => p.replace(/^\/api/, ""),
      },
    },
  },
});
