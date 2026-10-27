import type { MessageApi } from 'naive-ui'
import { api } from './api'

/**
 * Installs the latest Laranest: the daemon downloads it and restarts, so
 * wait for it to come back and reload the panel (it may be a new UI).
 */
export async function applyUpdate(message: MessageApi) {
  const info = message.loading('Downloading the new Laranest…', { duration: 0 })
  try {
    await api.updateLaranest()
    info.content = 'Restarting Laranest…'
    const deadline = Date.now() + 120_000
    let wentDown = false
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 1000))
      try {
        await api.health()
        if (wentDown) break
      } catch {
        wentDown = true
      }
    }
    location.reload()
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    info.destroy()
  }
}
