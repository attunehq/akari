import { type ReactNode, useEffect } from "react";

import { clearPreloadRecovery } from "./preload-recovery";

// RouteReady wraps every lazy page element so the preload-recovery marker
// survives the post-reload boot until that page actually commits. Clearing on
// root mount would re-arm the guard while the chunk is still in flight and
// turn a persistent asset failure into a reload loop.
export function RouteReady({
  title,
  children,
}: {
  title?: string;
  children: ReactNode;
}) {
  useEffect(() => {
    if (title) document.title = `${title} · akari`;
    clearPreloadRecovery(window);
  }, [title]);
  return children;
}
