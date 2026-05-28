// Mermaid renderer — runs on every page navigation under Material's
// instant navigation, so we need to re-init on each document$ event.

document$.subscribe(({ body }) => {
  // mermaid library is loaded by pymdownx.superfences custom_fence as
  // class="mermaid" code blocks. Material's Material theme bundles
  // a mermaid loader, but if you want to override it (e.g. lock to a
  // specific mermaid version), do it here.
  //
  // Default theming is "default" (light) / "dark" depending on
  // body[data-md-color-scheme]. We don't override that here.
})
