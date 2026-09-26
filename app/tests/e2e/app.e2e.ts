import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import AxeBuilder from '@axe-core/playwright'
import { _electron as electron, expect, test } from '@playwright/test'

const appRoot = join(__dirname, '../..')

test('setup screen is usable and meets WCAG 2.1 AA', async () => {
  const userData = mkdtempSync(join(tmpdir(), 'vault-e2e-'))
  const app = await electron.launch({
    args: ['.'],
    cwd: appRoot,
    env: { ...process.env, VAULT_USER_DATA: userData }
  })
  const page = await app.firstWindow()
  await expect(page.getByRole('heading', { name: 'Welcome to Vault' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Create cluster' })).toBeEnabled()
  await page.getByLabel(/Join an existing cluster/).check()
  await expect(page.getByLabel('Invite code')).toBeVisible()
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(results.violations, results.violations.map((v) => `${v.id}: ${v.help}`).join('\n')).toEqual([])
  await app.close()
})
