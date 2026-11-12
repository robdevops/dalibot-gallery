// Playwright configuration for the gallery's end-to-end tests. See README.md.
const { defineConfig } = require('playwright/test')
const path = require('path')

const port = Number(process.env.E2E_PORT || 18480)

module.exports = defineConfig({
  testDir: __dirname,
  testMatch: '*.spec.js',
  outputDir: path.join(__dirname, 'test-results'),
  globalSetup: path.join(__dirname, 'global-setup.js'),
  // One worker and a fixed file order: the "N new" test adds records to the
  // table, so it runs in the last file (zz-new.spec.js).
  workers: 1,
  fullyParallel: false,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: [['list']],
  use: {
    baseURL: process.env.E2E_URL || `http://127.0.0.1:${port}`,
    browserName: 'chromium',
    storageState: path.join(__dirname, '.auth', 'state.json'),
    viewport: { width: 1440, height: 900 },
    trace: 'off',
    video: 'off',
    screenshot: 'off',
  },
})
