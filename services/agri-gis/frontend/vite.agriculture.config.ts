import { fileURLToPath } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  root: fileURLToPath(new URL('./agriculture', import.meta.url)),
  base: '/agri/',
  publicDir: false,
  plugins: [vue(), {
    name: 'agriculture-no-legacy-business',
    generateBundle(_options, bundle) {
      const modules = Object.values(bundle).flatMap((item) => item.type === 'chunk' ? Object.keys(item.modules) : [])
      for (const module of modules) {
        if (/(?:\/src\/|\/node_modules\/(?:cesium|@cesium|tldraw|@tldraw)\/)/.test(module.replaceAll('\\', '/'))) this.error(`Unexpected legacy/3D/licensed canvas module in agricultural profile: ${module}`)
      }
      this.emitFile({ type: 'asset', fileName: 'agriculture-profile.json', source: JSON.stringify({ profile: 'vue-agriculture-farms', business_api: 'same-origin-platform-canonical-v1', map: 'openlayers-vector-only-no-external-basemap', modules: modules.map((module) => module.replace(fileURLToPath(new URL('.', import.meta.url)), './')).sort() }, null, 2) })
    },
  }],
  build: { outDir: '../dist-agriculture', emptyOutDir: true },
  // Development only. Production must serve /agri and /api on the same origin.
  server: { host: '127.0.0.1', port: 5174, proxy: { '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false } } },
})
