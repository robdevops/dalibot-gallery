// Starts the dev stack (moto, the fake Telegram API, the seed and the real
// gallery binary) through scripts/dev-stack.sh, signs in once through the
// login form and saves the cookie for the tests. Returns the teardown.
//
// With E2E_URL set, nothing is started: the tests run against that stack
// (E2E_TOKEN and E2E_CONTROL must describe it; see README.md).
const { spawn } = require('child_process')
const fs = require('fs')
const path = require('path')
const readline = require('readline')
const { chromium } = require('playwright')

const root = path.join(__dirname, '..')
const stateFile = path.join(__dirname, '.auth', 'state.json')

function startStack(port) {
  return new Promise((resolve, reject) => {
    // -refresh 5s is the shortest interval the gallery accepts; it only makes
    // the "N new" test quicker. Everything else is the production default.
    const child = spawn(path.join(root, 'scripts', 'dev-stack.sh'),
      ['-port', String(port), '-refresh', '5s'],
      { cwd: root, stdio: ['ignore', 'pipe', 'pipe'], detached: true })
    const log = fs.createWriteStream(path.join(__dirname, 'artifacts', 'dev-stack.log'))
    child.stderr.pipe(log)
    const timer = setTimeout(() => {
      stop(child)
      reject(new Error('the dev stack was not ready within 180 s; see e2e/artifacts/dev-stack.log'))
    }, 180_000)
    readline.createInterface({ input: child.stdout }).on('line', line => {
      let ready
      try { ready = JSON.parse(line) } catch { return }
      if (ready.ready) {
        clearTimeout(timer)
        resolve({ child, ready })
      }
    })
    child.on('exit', code => {
      clearTimeout(timer)
      reject(new Error(`the dev stack exited with ${code}; see e2e/artifacts/dev-stack.log`))
    })
  })
}

function stop(child) {
  return new Promise(resolve => {
    if (child.exitCode !== null) return resolve()
    child.on('exit', resolve)
    child.kill('SIGTERM') // the stack stops moto and the gallery itself
    setTimeout(() => { try { process.kill(-child.pid, 'SIGKILL') } catch {} }, 20_000).unref()
  })
}

module.exports = async config => {
  fs.mkdirSync(path.join(__dirname, 'artifacts'), { recursive: true })
  fs.mkdirSync(path.dirname(stateFile), { recursive: true })
  let child = null
  if (process.env.E2E_URL) {
    process.env.E2E_TOKEN ||= 'dev-gallery-token-0123456789'
    if (!process.env.E2E_CONTROL) {
      const u = new URL(process.env.E2E_URL)
      u.port = String(Number(u.port) + 1)
      process.env.E2E_CONTROL = u.origin
    }
  } else {
    const started = await startStack(Number(process.env.E2E_PORT || 18480))
    child = started.child
    process.env.E2E_URL = started.ready.url
    process.env.E2E_TOKEN = started.ready.token
    process.env.E2E_CONTROL = started.ready.control
  }

  // Sign in the way a visitor does, and keep the cookie.
  const browser = await chromium.launch()
  try {
    const page = await browser.newPage({ baseURL: process.env.E2E_URL })
    await page.goto('/login')
    await page.getByLabel('Access token').fill(process.env.E2E_TOKEN)
    await page.getByRole('button', { name: 'Sign in' }).click()
    await page.waitForURL(u => u.pathname === '/')
    await page.context().storageState({ path: stateFile })
  } catch (err) {
    if (child) await stop(child)
    throw err
  } finally {
    await browser.close()
  }

  return async () => {
    if (child) await stop(child)
  }
}
