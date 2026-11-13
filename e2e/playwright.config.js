// Playwright configuration for the gallery's end-to-end tests. See README.md.
const { defineConfig } = require('playwright/test')
const path = require('path')

const port = Number(process.env.E2E_PORT || 18480)

module.exports = defineConfig({
  testDir: __dirname,
  outputDir: path.join(__dirname, 'test-results'),
  globalSetup: path.join(__dirname, 'global-setup.js'),
  // The stack's state is shared, so the specs that change it or measure it run
  // one after another, in the order of "dependencies" below: access.spec.js and
  // login.spec.js use up the token guesses a client may make per minute, admin.spec.js
  // hides images, zz-new.spec.js adds records to the table, perf.spec.js measures
  // time. The rest only read, and their tests run side by side.
  workers: Number(process.env.E2E_WORKERS || 4),
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  projects: [
    { name: 'read', testMatch: ['grid', 'lightbox', 'noscript', 'screenshots'].map(n => `${n}.spec.js`), fullyParallel: true },
    ...['perf', 'login', 'access', 'admin', 'zz-new'].map((n, i, all) => ({
      name: n, testMatch: `${n}.spec.js`, dependencies: [i ? all[i - 1] : 'read'],
    })),
  ],
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
