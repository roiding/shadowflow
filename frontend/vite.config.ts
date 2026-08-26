import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// This file runs under Node, but the project deliberately has no @types/node
// dependency; declare the single global we read.
declare const process: { env: Record<string, string | undefined> }

export default defineConfig({
  plugins: [react()],
  server: {
    // Bind to localhost by default: the proxy below reaches the backend's
    // loopback port, so exposing the dev server on 0.0.0.0 would let anyone
    // on the LAN read all data without a token (and expose source via /@fs).
    // Set VITE_EXPOSE=1 for on-device debugging when you really need it.
    host: process.env.VITE_EXPOSE === '1' ? '0.0.0.0' : 'localhost',
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/health': 'http://127.0.0.1:8080',
    },
  },
})
