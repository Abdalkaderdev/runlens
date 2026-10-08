import { defineConfig } from 'vite';

const target = process.env.RUNLENS_URL ?? 'http://127.0.0.1:4318';

export default defineConfig({
  build: { outDir: '../internal/ui/dist', emptyOutDir: true, assetsInlineLimit: 0 },
  server: { proxy: { '/api': target, '/mcp': target } },
});
