// Starts two dev stacks (each: moto, the fake Telegram API, the seed and the
// real gallery binary) through scripts/dev-stack.sh, signs in once through
// the login form of the first and saves the cookie for the tests. Returns the
// teardown.
//
//   - the main stack, on E2E_PORT: viewing needs the token, and there is an
//     admin token. Every spec but access.spec.js uses only this one.
//   - the public stack, on E2E_PORT + 2: GALLERY_PUBLIC=1 with the same admin
//     token, for access.spec.js.
//
// With E2E_URL set, nothing is started: the tests run against that stack
// (E2E_TOKEN, E2E_ADMIN_TOKEN and E2E_CONTROL must describe it; see
// README.md), and the public-mode tests run only if E2E_PUBLIC_URL is set too.
const { spawn } = require('child_process')
const fs = require('fs')
const path = require('path')
const readline = require('readline')
const { chromium } = require('playwright')

const root = path.join(__dirname, '..')
const stateFile = path.join(__dirname, '.auth', 'state.json')

const ADMIN_TOKEN = 'dev-admin-token-0123456789' // devstack.AdminToken

function startStack(port, extra, logName) {
  return new Promise((resolve, reject) => {
    // -refresh 5s is the shortest interval the gallery accepts; it only makes
    // the "N new" test quicker. Everything else is the production default.
    const child = spawn(path.join(root, 'scripts', 'dev-stack.sh'),
      ['-port', String(port), '-refresh', '5s', '-admin-token', ADMIN_TOKEN, ...extra],
      { cwd: root, stdio: ['ignore', 'pipe', 'pipe'], detached: true })
    const log = fs.createWriteStream(path.join(__dirname, 'artifacts', logName))
    child.stderr.pipe(log)
    const timer = setTimeout(() => {
      stop(child)
      reject(new Error('the dev stack was not ready within 180 s; see e2e/artifacts/' + logName))
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
      reject(new Error(`the dev stack exited with ${code}; see e2e/artifacts/${logName}`))
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
  const children = []
  const stopAll = () => Promise.all(children.map(stop))
  if (process.env.E2E_URL) {
    process.env.E2E_TOKEN ||= 'dev-gallery-token-0123456789'
    process.env.E2E_ADMIN_TOKEN ||= ADMIN_TOKEN
    if (!process.env.E2E_CONTROL) {
      const u = new URL(process.env.E2E_URL)
      u.port = String(Number(u.port) + 1)
      process.env.E2E_CONTROL = u.origin
    }
  } else {
    const port = Number(process.env.E2E_PORT || 18480)
    try {
      const started = await startStack(port, [], 'dev-stack.log')
      children.push(started.child)
      process.env.E2E_URL = started.ready.url
      process.env.E2E_TOKEN = started.ready.token
      process.env.E2E_ADMIN_TOKEN = started.ready.admin_token
      process.env.E2E_CONTROL = started.ready.control
      const open = await startStack(port + 2, ['-auth=false'], 'dev-stack-public.log')
      children.push(open.child)
      if (!open.ready.public || open.ready.token) throw new Error('the second dev stack is not public')
      process.env.E2E_PUBLIC_URL = open.ready.url
    } catch (err) {
      await stopAll()
      throw err
    }
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
    await stopAll()
    throw err
  } finally {
    await browser.close()
  }

  return stopAll
}
