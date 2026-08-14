import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// Separate from vite.config.ts on purpose: vite.config.ts's defineConfig
// (from 'vite') doesn't know about the `test` key, and merging them risks
// leaking test-only config into the production build. Scope kept minimal —
// this is only for the new shared components (NumberInput etc.), not a
// retrofit of the existing untested pages.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./vitest.setup.ts'],
    globals: false,
  },
})
