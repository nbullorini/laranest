import { h } from 'vue'
import type { DialogApi, MessageApi } from 'naive-ui'
import { api, type WSLStatus } from './api'

/** Asks before touching Windows' .wslconfig (it affects every WSL distro). */
export function confirmKeepAlive(
  dialog: DialogApi, message: MessageApi, st: WSLStatus, done: (st: WSLStatus) => void,
) {
  dialog.warning({
    title: 'Keep WSL running',
    content: () => h('div', { class: 'stack', style: 'gap:8px' }, [
      h('p', { style: 'margin:0' }, [
        'WSL shuts the distro down a few seconds after the last terminal closes, and Laranest and your apps go down with it. ',
        'To prevent it, this line is added to ', h('code', st.config_file), ':',
      ]),
      h('pre', { class: 'diff' }, '[general]\ninstanceIdleTimeout=-1'),
      h('p', { class: 'muted small', style: 'margin:0' }, 'It affects all your WSL distros. A backup is kept (.laranest.bak) and it can be reverted from Settings. It takes effect the next time WSL starts (or after "wsl --shutdown" from Windows).'),
    ]),
    positiveText: 'Apply',
    negativeText: 'Cancel',
    onPositiveClick: async () => {
      try {
        done(await api.setWSL({ keep_alive: true }))
        message.success('Done: it takes effect when WSL restarts')
      } catch (e) {
        message.error((e as Error).message)
      }
    },
  })
}
