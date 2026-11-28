import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import frontend from '../../../../../frontend/vite.config'

const workspace = fileURLToPath(new URL('../../../../../', import.meta.url))
const frontendRequire = createRequire(new URL('../../../../../frontend/package.json', import.meta.url))
export default {
  ...frontend,
  resolve: { ...frontend.resolve, alias: { '@vue/test-utils': frontendRequire.resolve('@vue/test-utils') } },
  server: { ...frontend.server, fs: { allow: [workspace] } },
  test: {
    ...frontend.test,
    globals: true,
    dir: fileURLToPath(new URL('.', import.meta.url)),
    include: ['**/teacher_channel_registration.test.ts'],
  },
}
