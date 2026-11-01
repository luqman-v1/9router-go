// The Material Symbols font is declared in `src/index.css` and shipped from
// `src/assets`, not imported here. The npm package's stylesheet points at its
// own 3.9 MB variable font with every axis live; the vendored copy is the
// same font instanced down to the axes the UI pins (349 KB) and still carries
// every glyph, so no icon name can render as tofu.
import { mount } from 'svelte'
import App from './App.svelte'
import './index.css'
import { registerServiceWorker } from './lib/pwa'
import { loadProviderCatalog } from './lib/providers'

registerServiceWorker()
// The provider catalog comes from the Go registry, so the app cannot render a
// provider card before it arrives. A failure here is fatal rather than
// degraded: every provider view would otherwise render empty and look like a
// bug in the view rather than a missing fetch.
loadProviderCatalog().catch((err) => {
  document.getElementById('app')!.innerHTML =
    '<div style="padding:2rem;font-family:system-ui">Failed to load the provider catalog: ' + String(err) + '</div>'
})

const app = mount(App, {
  target: document.getElementById('app')!,
})

export default app
