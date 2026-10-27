// UI walkthrough for regression.sh. Needs playwright-core and Chrome:
//   PW_DIR=<dir with node_modules/playwright-core> node ui.mjs http://127.0.0.1:9180
import { createRequire } from 'node:module'
const require = createRequire((process.env.PW_DIR ?? '.') + '/')
const { chromium } = require('playwright-core')

const base = process.argv[2]
const browser = await chromium.launch({ executablePath: process.env.CHROME ?? '/usr/bin/google-chrome', headless: true })
const page = await browser.newPage({ viewport: { width: 1280, height: 860 }, colorScheme: 'dark' })
const errors = []
page.on('pageerror', (e) => errors.push('pageerror: ' + e.message))
page.on('console', (m) => m.type() === 'error' && !m.text().includes('favicon') && errors.push('console: ' + m.text()))

const steps = []
async function step(name, fn) {
  try {
    await fn()
    steps.push('ok   ' + name)
  } catch (e) {
    steps.push('FAIL ' + name + ': ' + e.message.split('\n')[0])
  }
}

await page.goto(base + '/')
await step('apps table with a group', async () => {
  await page.locator('.apps-table').waitFor()
  await page.locator('.group-row', { hasText: 'Other group' }).waitFor()
})
await step('detail: tabs', async () => {
  await page.locator('.app-row').first().click()
  for (const tab of ['Processes', 'Logs', 'Settings', 'Overview']) {
    await page.locator('.n-tabs-tab', { hasText: tab }).click()
    await page.waitForTimeout(250)
  }
  await page.keyboard.press('Escape')
})
await step('live logs in the UI', async () => {
  await page.locator('.app-row').first().click()
  await page.locator('.n-tabs-tab', { hasText: 'Logs' }).click()
  await page.locator('.logs .line').first().waitFor({ timeout: 8000 })
  await page.keyboard.press('Escape')
})
await step('commands: run one and see its output', async () => {
  await page.locator('.app-row').first().click()
  await page.locator('.n-tabs-tab', { hasText: 'Commands' }).click()
  await page.locator('.n-input-group input').first().fill('php artisan --version')
  await page.getByRole('button', { name: 'Run', exact: true }).click()
  await page.locator('pre.output', { hasText: 'Laravel Framework' }).waitFor({ timeout: 30000 })
  await page.keyboard.press('Escape')
})
await step('add app: search', async () => {
  await page.getByRole('button', { name: 'Add app' }).first().click()
  await page.locator('.n-modal').waitFor()
  await page.locator('.n-modal input').first().fill('demo')
  await page.waitForTimeout(400)
  await page.keyboard.press('Escape')
})
await step('PHP view', async () => {
  await page.getByRole('button', { name: 'PHP', exact: true }).click()
  await page.locator('.versions').waitFor()
  await page.keyboard.press('Escape')
})
await step('settings', async () => {
  await page.locator('.topbar button').nth(1).click()
  await page.getByText('HTTPS certificates').waitFor()
  await page.keyboard.press('Escape')
})
await step('no JavaScript errors', async () => {
  if (errors.length) throw new Error(errors.join(' | '))
})

console.log(steps.join('\n'))
await browser.close()
process.exit(steps.some((s) => s.startsWith('FAIL')) ? 1 : 0)
