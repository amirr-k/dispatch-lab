// GitHub Pages serves a project site under /<repo>/, so every internal link
// and the router's own path matching have to account for that prefix - it's
// "/" locally and in a custom-domain deploy, and "/<repo>/" on a GitHub
// Pages project page. Vite's own BASE_URL (set via vite.config.ts's `base`)
// is the single source of truth for which one is active.
const BASE = import.meta.env.BASE_URL;

// withBase turns a root-relative path ("/compare", "/replay/abc") into one
// that resolves correctly under BASE, however it's configured.
export function withBase(path: string): string {
  const trimmedBase = BASE.endsWith('/') ? BASE.slice(0, -1) : BASE;
  const trimmedPath = path.startsWith('/') ? path : `/${path}`;
  return `${trimmedBase}${trimmedPath}` || '/';
}

// stripBase is withBase's inverse: it turns the browser's actual pathname
// back into the root-relative form the router's own route() matches against,
// so route matching doesn't need to know about the deploy prefix at all.
export function stripBase(pathname: string): string {
  const trimmedBase = BASE.endsWith('/') ? BASE.slice(0, -1) : BASE;
  if (trimmedBase && pathname.startsWith(trimmedBase)) {
    const rest = pathname.slice(trimmedBase.length);
    return rest.startsWith('/') ? rest : `/${rest}`;
  }
  return pathname;
}
