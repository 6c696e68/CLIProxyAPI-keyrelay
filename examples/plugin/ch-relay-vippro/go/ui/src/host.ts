// The management panel renders this page in a same-origin iframe under a fixed
// header. Mirror the panel theme and reserve the band its floating actions cover.

export type ThemeName = 'light' | 'dark'

function panelTheme(): ThemeName | null {
  try {
    if (window.parent === window) return null
    const value = window.parent.document.documentElement.getAttribute('data-theme')
    return value === 'dark' ? 'dark' : 'light'
  } catch {
    return null
  }
}

export function resolveTheme(): ThemeName {
  const host = panelTheme()
  if (host) return host
  const stored = localStorage.getItem('vippro.dashboard.theme')
  if (stored === 'dark' || stored === 'light') return stored
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
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

export function bootHost(onTheme: (value: ThemeName) => void) {
  const root = document.documentElement
  root.dataset.theme = resolveTheme()
  applyShellMetrics()
  window.addEventListener('resize', applyShellMetrics)
  try {
    if (window.parent === window) return
    const parentRoot = window.parent.document.documentElement
    new MutationObserver(() => {
      const value = panelTheme()
      if (value) onTheme(value)
    }).observe(parentRoot, { attributes: true, attributeFilter: ['data-theme', 'class'] })
  } catch {
    // Cross-origin hosts cannot be observed; keep the initial theme.
  }
}
