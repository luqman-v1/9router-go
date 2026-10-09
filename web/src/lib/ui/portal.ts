/**
 * Svelte action that reparents a dropdown panel to `<body>` while it is open.
 *
 * Every dropdown panel in this dashboard is `position: fixed` and placed
 * against its trigger's measured rect by `menuPosition`. That is correct only
 * while the panel's nearest ancestor chain contains no containing block, and a
 * `filter`, `transform`, `perspective`, `contain` or `backdrop-filter` all
 * create one. When it does, `left`/`top` resolve against that ancestor's
 * padding box instead of the window, so a panel computed as perfectly in-view
 * renders off-screen and receives no clicks.
 *
 * The top bar's `<header>` carries `backdrop-blur-xl`, so the account menu
 * behind it reported `left=1491, top=55` in a 1440px window: the panel was
 * fully off-screen and the menu opened in response to nothing (issue #235).
 * Portalling removes the dependency on the ancestor instead of subtracting its
 * offset, which would have to be re-derived for every new trigger.
 *
 * The node is detached again on destroy, which Svelte also runs when the
 * panel's `{#if open}` block tears the element down.
 */

export function portal(node: HTMLElement): { destroy: () => void } {
  document.body.appendChild(node)
  return {
    destroy() {
      node.remove()
    },
  }
}