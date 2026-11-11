import { createApp } from 'vue'
import App from '@app'
import './styles.css'
import { applyTheme, bootHost, resolveTheme } from './lib/host'

// Apply host metrics and theme before mount so the first paint has no flash.
applyTheme(resolveTheme())
bootHost(applyTheme)
createApp(App).mount('#app')
