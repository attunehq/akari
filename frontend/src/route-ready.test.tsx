import { render, screen } from "@testing-library/react";
import { lazy, type ReactElement, Suspense } from "react";
import { afterEach, expect, it } from "vitest";

import { preloadReloadKey } from "./preload-recovery";
import { RouteReady } from "./route-ready";

afterEach(() => {
  sessionStorage.removeItem(preloadReloadKey);
});

it("keeps the preload marker until the lazy child commits", async () => {
  sessionStorage.setItem(preloadReloadKey, "https://akari.test/overview");

  let resolvePage!: (value: { default: () => ReactElement }) => void;
  const pagePromise = new Promise<{ default: () => ReactElement }>(
    (resolve) => {
      resolvePage = resolve;
    },
  );
  const LazyPage = lazy(() => pagePromise);

  render(
    <Suspense fallback={<div>Loading view...</div>}>
      <RouteReady title="Overview">
        <LazyPage />
      </RouteReady>
    </Suspense>,
  );

  expect(screen.getByText("Loading view...")).toBeInTheDocument();
  expect(sessionStorage.getItem(preloadReloadKey)).toBe(
    "https://akari.test/overview",
  );

  resolvePage({
    default: () => <div>Overview ready</div>,
  });

  expect(await screen.findByText("Overview ready")).toBeInTheDocument();
  expect(document.title).toBe("Overview · akari");
  expect(sessionStorage.getItem(preloadReloadKey)).toBeNull();
});
