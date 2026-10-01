import { defineConfig } from 'vitest/config'

// Only the plain TypeScript in src/lib is unit tested; screens are checked in the simulator.
export default defineConfig({ test: { include: ['src/lib/**/*.test.ts'] } })
