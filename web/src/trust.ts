import type { MessageApi } from 'naive-ui'
import { api, type TrustStatus } from './api'

/** Windows and WSL: adds the CA to the Windows user (Windows shows a confirmation dialog). */
export async function trustWindows(message: MessageApi): Promise<TrustStatus | null> {
  const info = message.loading('Accept the Windows dialog asking to install the certificate…', { duration: 0 })
  try {
    const st = await api.trustWindows()
    message.success('Done: restart the browser if it was open')
    return st
  } catch (e) {
    message.error((e as Error).message)
    return null
  } finally {
    info.destroy()
  }
}
