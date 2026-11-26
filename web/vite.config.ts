import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

declare const process: { env: { VITE_API_PROXY_TARGET?: string } }

declare global {
  interface ImportMeta {
    dirname: string
  }
}

const target = process.env.VITE_API_PROXY_TARGET || 'http://localhost:8080'
const backendPaths = ['/api', '/healthz', '/readyz', '/openapi.json', '/openapi.yaml']

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': `${import.meta.dirname}/src` },
  },
  server: {
    proxy: Object.fromEntries(backendPaths.map((path) => [path, { target, changeOrigin: true }])),
  },
})
