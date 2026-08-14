import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

// globals: false in vitest.config.ts means RTL's usual auto-registered
// afterEach(cleanup) (which hooks into a global `afterEach`) never fires, so
// it's wired up explicitly here instead.
afterEach(() => {
  cleanup()
})
