// Host integration for the plugin page. The management panel renders this app
// inside a same-origin iframe under its own fixed header, so the page must
// follow the panel theme and reserve the space the header floats over.

export type ThemeName = 'light' | 'dark'

const THEME_STORAGE = 'keyshop.admin.theme'

// panelTheme reads the panel's data-theme. The panel uses "white" for its light
// theme and no attribute for the default light theme.
function panelTheme(): ThemeName | null {
  try {
    if (window.parent === window) return null
    const value = window.parent.document.documentElement.getAttribute('data-theme')
    return value === 'dark' ? 'dark' : 'light'
  } catch {
    return null
  }
}

// resolveTheme prefers the panel theme because the panel owns the only visible
// theme switch. A stored choice only applies when the page is opened alone.
export function resolveTheme(): ThemeName {
  const host = panelTheme()
  if (host) return host
  const stored = localStorage.getItem(THEME_STORAGE)
  if (stored === 'dark' || stored === 'light') return stored
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export function applyTheme(value: ThemeName) {
  document.documentElement.dataset.theme = value
}

// watchHostTheme calls back whenever the panel switches theme.
export function watchHostTheme(onChange: (value: ThemeName) => void): () => void {
  let root: HTMLElement | null = null
  try {
    if (window.parent === window) return () => {}
    root = window.parent.document.documentElement
  } catch {
    return () => {}
  }
  const observer = new MutationObserver(() => {
    const value = panelTheme()
    if (value) onChange(value)
  })
  observer.observe(root, { attributes: true, attributeFilter: ['data-theme', 'class'] })
  return () => observer.disconnect()
}

// applyShellMetrics reserves the vertical band the panel header floats over.
// The iframe starts at y=0 on desktop, so content would otherwise sit under the
// floating panel actions. On mobile the panel already insets the iframe.
export function applyShellMetrics() {
  const root = document.documentElement
  let embedded = false
  let top = 0
  try {
    const frame = window.frameElement
    if (frame && window.parent !== window) {
      embedded = true
      const style = window.parent.getComputedStyle(window.parent.document.documentElement)
      const header = parseFloat(style.getPropertyValue('--header-height')) || 80
      const frameTop = frame.getBoundingClientRect().top || 0
      top = Math.max(0, Math.round(header - 10 - frameTop))
    }
  } catch {
    // Cross-origin hosts expose no metrics; no reserve is the safe default.
  }
  root.dataset.embedded = embedded ? 'true' : 'false'
  root.style.setProperty('--chrome-top', `${top}px`)
}

// bootHost runs the host bridge once, before the first paint, and keeps it in
// sync when the panel theme or the frame size changes.
export function bootHost(onTheme: (value: ThemeName) => void) {
  applyShellMetrics()
  window.addEventListener('resize', applyShellMetrics)
  const stop = watchHostTheme(onTheme)
  window.addEventListener('pagehide', stop, { once: true })
}
