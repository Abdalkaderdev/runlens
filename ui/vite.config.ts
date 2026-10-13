import { defineConfig } from 'vite';
import { resolve } from 'node:path';

const target = process.env.RUNLENS_URL ?? 'http://127.0.0.1:4318';

export default defineConfig({
  build: {
    outDir: '../internal/ui/dist',
    emptyOutDir: true,
    assetsInlineLimit: 0,
    rollupOptions: { input: { main: resolve(import.meta.dirname, 'index.html'), agents: resolve(import.meta.dirname, 'agents.html') } },
  },
  server: { proxy: { '/api': target, '/mcp': target } },
});
