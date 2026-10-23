// Helpers for driving the copy that up.sh started. Import from a driver script:
//
//   import * as e2e from '<skill>/scripts/lib.mjs'
//   const ctx = await e2e.open()
//   await e2e.go(ctx, '/transactions')
//   await e2e.clickText(ctx, 'Record', { within: 'section.pending li', nth: 0 })
//   await e2e.settle(ctx)
//   console.log(await e2e.fields(ctx))
//   await e2e.close(ctx)
//
// Clicks are dispatched in the page (element.click()), not with the mouse. In
// this setup headless Chrome sometimes stops delivering mouse input to the page
// altogether after the login redirect — no pointerdown ever arrives, on every
// route, while DOM clicks keep working. A test that silently does nothing looks
// exactly like an app bug, which cost hours to tell apart, so the helpers avoid
// the mouse and `mouseWorks` exists to check before relying on it.

import { createRequire } from 'node:module'
import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'

const CACHE = join(process.env.XDG_CACHE_HOME || join(homedir(), '.cache'), 'stockbook-e2e')
const puppeteer = createRequire(join(CACHE, 'package.json'))('puppeteer-core')

const CHROME =
  process.env.E2E_CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'

/** The run up.sh wrote: BASE, API, E2E_USER, E2E_PASSWORD, E2E_DIR, … */
export function readEnv() {
  const dir = process.env.E2E_DIR || join(process.env.TMPDIR || '/tmp', 'stockbook-e2e')
  const env = {}
  for (const line of readFileSync(join(dir, 'env'), 'utf8').split('\n')) {
    const at = line.indexOf('=')
    if (at > 0) env[line.slice(0, at)] = line.slice(at + 1)
  }
  return env
}

/**
 * Launches Chrome and logs in through the real login form.
 *
 * Everything worth knowing afterwards is collected on the context: every /api/
 * request and its status, page errors, console errors, and any dialog. A
 * confirm() is dismissed unless `dialogs: 'accept'` — an unexpected one left
 * open would block the page, and dismissing is the safe default for "Delete?".
 */
export async function open({ width = 1280, height = 1000, dialogs = 'dismiss' } = {}) {
  const env = readEnv()
  const browser = await puppeteer.launch({ executablePath: CHROME, headless: 'new' })
  const page = await browser.newPage()
  await page.setViewport({ width, height })
  // inflight maps each open request to when it started, so settle() can wait
  // for what the last action caused and nothing older — see settle().
  const ctx = { browser, page, env, net: [], errors: [], dialogs: [], inflight: new Map(), mark: 0 }
  page.on('request', (r) => ctx.inflight.set(r, Date.now()))
  page.on('requestfinished', (r) => ctx.inflight.delete(r))
  page.on('requestfailed', (r) => ctx.inflight.delete(r))

  // By path prefix: Vite serves the frontend's own src/api/client.ts as a module,
  // which a substring match would log as an API call.
  const apiPath = (url) => {
    const path = url.replace(env.BASE, '')
    return path.startsWith('/api/') ? path : null
  }
  page.on('request', (r) => {
    const path = apiPath(r.url())
    if (path) ctx.net.push(`→ ${r.method()} ${path}`)
  })
  page.on('response', (r) => {
    const path = apiPath(r.url())
    if (path) ctx.net.push(`← ${r.status()} ${path}`)
  })
  page.on('pageerror', (e) => ctx.errors.push(`pageerror: ${e.message}`))
  page.on('console', (m) => {
    if (m.type() === 'error') ctx.errors.push(`console: ${m.text()}`)
  })
  page.on('dialog', async (d) => {
    ctx.dialogs.push(`${d.type()}: ${d.message()}`)
    await (dialogs === 'accept' ? d.accept() : d.dismiss())
  })

  await page.goto(`${env.BASE}/login`, { waitUntil: 'networkidle0' })
  const [user, password] = await page.$$('input')
  await user.type(env.E2E_USER)
  await password.type(env.E2E_PASSWORD)
  await page.keyboard.press('Enter')
  await page.waitForFunction(() => location.pathname !== '/login', { timeout: 15000 })
  return ctx
}

/** Navigates to an app path and waits for its requests to finish. */
export async function go(ctx, path) {
  ctx.mark = Date.now()
  await ctx.page.goto(`${ctx.env.BASE}${path}`, { waitUntil: 'networkidle0' })
  // networkidle0 means the new document's own requests are done, so anything
  // still listed belonged to the page just left. Chrome drops those without
  // puppeteer reporting them finished or failed, and left in the map they would
  // hold settle() open forever — the Holdings page's quote refresh, fired on
  // arrival after login, is the one that does.
  ctx.inflight.clear()
}

/**
 * Clicks the nth button or link whose text contains `text`, optionally only
 * among those inside `within`. Throws, listing what was there, when nothing
 * matches — a click that found no target must not pass as one that did.
 */
export async function clickText(ctx, text, { within = 'body', nth = 0 } = {}) {
  ctx.mark = Date.now()
  const result = await ctx.page.evaluate(
    (text, within, nth) => {
      const scopes = [...document.querySelectorAll(within)]
      const candidates = scopes.flatMap((s) => [...s.querySelectorAll('button, a')])
      const hits = candidates.filter((el) => el.textContent.trim().includes(text))
      if (hits.length <= nth) {
        return { ok: false, seen: candidates.map((el) => el.textContent.trim()).filter(Boolean) }
      }
      const el = hits[nth]
      if (el.disabled) return { ok: false, disabled: true }
      el.scrollIntoView({ block: 'center' })
      el.click()
      return { ok: true }
    },
    text,
    within,
    nth,
  )
  if (!result.ok) {
    throw new Error(
      result.disabled
        ? `"${text}" (#${nth} in ${within}) is disabled`
        : `no "${text}" #${nth} in ${within}; saw: ${JSON.stringify(result.seen)}`,
    )
  }
}

/** Sets an input or select the way a person would, so v-model sees it. */
export async function fill(ctx, selector, value) {
  ctx.mark = Date.now()
  await ctx.page.$eval(
    selector,
    (el, value) => {
      el.focus()
      el.value = value
      el.dispatchEvent(new Event('input', { bubbles: true }))
      el.dispatchEvent(new Event('change', { bubbles: true }))
    },
    String(value),
  )
}

/**
 * Waits for what the last go/clickText/fill set off to finish: every request
 * started since then, including ones those requests chain into, and no button
 * left busy ("Fetching price…"). Then a short pause for Vue to render.
 *
 * Requests older than the last action are deliberately not waited for. Holdings
 * and Research refresh quotes and news on arrival — and the login lands on
 * Holdings — so a whole-page network-idle wait sits behind a provider round
 * trip for every instrument that has nothing to do with what is being tested.
 */
export async function settle(ctx, { timeout = 60000 } = {}) {
  const deadline = Date.now() + timeout
  let quietSince = Date.now()
  for (;;) {
    const busy = [...ctx.inflight.values()].some((started) => started >= ctx.mark)
    if (busy) quietSince = Date.now()
    else if (Date.now() - quietSince >= 400) break
    if (Date.now() > deadline) {
      const open = [...ctx.inflight.entries()]
        .filter(([, started]) => started >= ctx.mark)
        .map(([r]) => `${r.method()} ${r.url()}`)
      throw new Error(`settle: still waiting after ${timeout}ms on ${open.join(', ')}`)
    }
    await new Promise((r) => setTimeout(r, 50))
  }
  await ctx.page
    .waitForFunction(() => ![...document.querySelectorAll('button')].some((b) => b.disabled && /…/.test(b.textContent)), { timeout })
    .catch(() => {})
  await new Promise((r) => setTimeout(r, 300))
}

/**
 * Every labelled field in a form as { label: value }. The instrument picker
 * reports what it shows, since it has no single input that holds the answer:
 * the chosen instrument, "(nothing chosen)", or "(search open: <query>)" when
 * its dropdown is showing — which is a state worth seeing, because a dropdown
 * left open hides an instrument that was set.
 */
export async function fields(ctx, form = 'form') {
  return ctx.page.$eval(form, (f) => {
    const out = {}
    for (const field of f.querySelectorAll('.field')) {
      const label = field.querySelector('label')?.textContent.trim().replace(/\s+/g, ' ')
      if (!label) continue
      const picker = field.querySelector('.picker')
      if (picker) {
        const chosen = picker.querySelector('.chosen')
        const query = picker.querySelector('input')?.value ?? ''
        out[label] = chosen
          ? chosen.innerText.replace(/\s+/g, ' ').replace(/ change$/, '')
          : picker.querySelector('.results')
            ? `(search open: "${query}")`
            : '(nothing chosen)'
        continue
      }
      const ctl = field.querySelector('input:not([type=checkbox]), select')
      if (ctl) out[label] = ctl.value
      for (const box of field.querySelectorAll('input[type=checkbox]')) {
        out[box.parentElement.textContent.trim()] = box.checked
      }
    }
    return out
  })
}

/** The visible text of whatever matches, one string per element. */
export async function texts(ctx, selector) {
  return ctx.page.$$eval(selector, (els) => els.map((e) => e.innerText.replace(/\s+/g, ' ').trim()))
}

/** Screenshot into the run directory; returns the path to Read. */
export async function shot(ctx, name, { selector, fullPage = false } = {}) {
  const path = join(ctx.env.E2E_DIR, `shot-${name}.png`)
  if (selector) await (await ctx.page.$(selector)).screenshot({ path })
  else await ctx.page.screenshot({ path, fullPage })
  return path
}

/**
 * Whether real mouse input reaches the page right now. Use before any test
 * that has to be about the mouse itself (hover, drag); otherwise prefer
 * clickText.
 */
export async function mouseWorks(ctx) {
  await ctx.page.evaluate(() => {
    window.__e2eHits = 0
    window.addEventListener('pointerdown', () => window.__e2eHits++, true)
  })
  await ctx.page.mouse.click(5, 5)
  await new Promise((r) => setTimeout(r, 200))
  return ctx.page.evaluate(() => window.__e2eHits > 0)
}

export async function close(ctx) {
  await ctx.browser.close()
}
