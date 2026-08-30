export const preloadReloadKey = "akari:preload-reload";

type PreloadRecoveryWindow = {
  addEventListener(type: string, listener: EventListener): void;
  location: Pick<Location, "href" | "reload">;
  sessionStorage: Pick<Storage, "getItem" | "setItem" | "removeItem">;
};

// A deploy can replace the server while a browser is fetching a lazy route's
// hashed CSS or JavaScript. Retry that interrupted navigation once against the
// new instance. Keep the marker until the route renders so a persistent asset
// failure reaches the router error boundary instead of entering a reload loop.
export function installPreloadRecovery(target: PreloadRecoveryWindow) {
  target.addEventListener("vite:preloadError", (event) => {
    const route = target.location.href;
    try {
      if (target.sessionStorage.getItem(preloadReloadKey) === route) return;
      target.sessionStorage.setItem(preloadReloadKey, route);
    } catch {
      return;
    }
    event.preventDefault();
    target.location.reload();
  });
}

export function clearPreloadRecovery(target: PreloadRecoveryWindow) {
  try {
    target.sessionStorage.removeItem(preloadReloadKey);
  } catch {
    return;
  }
}
