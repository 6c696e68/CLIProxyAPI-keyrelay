import { fileURLToPath, URL } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'
import { viteSingleFile } from 'vite-plugin-singlefile'

export default defineConfig(({ command }) => ({
  plugins: [vue(), tailwindcss(), viteSingleFile()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
      '@app': fileURLToPath(new URL(command === 'serve' ? './src/App.dev.vue' : './src/App.vue', import.meta.url)),
    },
  },
  server: { host: '127.0.0.1', port: 5174, strictPort: true },
  base: './',
  build: {
    cssCodeSplit: false,
    assetsInlineLimit: 100_000_000,
  },
}))
