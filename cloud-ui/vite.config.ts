import { defineConfig } from 'vitest/config';
import { loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

// VITE_API_PROXY_TARGET overrides the proxy destination during `pnpm dev`.
// Default points at the deployed dc-api on harvester-dev via HTTPS — the
// office network firewalls port 80, only 443 is allowed.
// secure:false tells Vite/http-proxy to accept the self-signed ingress-nginx
// fake cert that the cluster currently serves on .37:443 (we haven't issued
// a proper cert yet — see F18 in FOLLOWUPS).
//
// Reads from .env.local via Vite's loadEnv (process.env alone doesn't pick
// up .env files for vite.config.ts — only import.meta.env does at runtime).
// Set VITE_API_PROXY_TARGET=http://localhost:8080 in .env.local when
// running dc-api locally for BFF demos.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  const proxyTarget =
    env.VITE_API_PROXY_TARGET ?? 'https://dcapi.lk.internal.wso2.com';
  return {
    plugins: [react()],
    server: {
      proxy: {
        '/v1': {
          target: proxyTarget,
          changeOrigin: true,
          secure: false,
        },
        '/healthz': {
          target: proxyTarget,
          changeOrigin: true,
          secure: false,
        },
      },
    },
    test: {
      environment: 'jsdom',
      globals: true,
      setupFiles: ['./src/test/setup.ts'],
      css: false,
    },
  };
});
