import { expect, it, vi } from "vitest";

import {
  clearPreloadRecovery,
  installPreloadRecovery,
  preloadReloadKey,
} from "./preload-recovery";

function recoveryWindow() {
  const values = new Map<string, string>();
  let listener: EventListener | undefined;
  const reload = vi.fn();
  const target = {
    addEventListener(_type: string, next: EventListener) {
      listener = next;
    },
    location: { href: "https://akari.test/overview", reload },
    sessionStorage: {
      getItem(key: string) {
        return values.get(key) ?? null;
      },
      setItem(key: string, value: string) {
        values.set(key, value);
      },
      removeItem(key: string) {
        values.delete(key);
      },
    },
  };
  return {
    target,
    values,
    reload,
    dispatch: (event: Event) => listener?.(event),
  };
}

it("reloads once after a lazy asset preload fails", () => {
  const { target, values, reload, dispatch } = recoveryWindow();
  installPreloadRecovery(target);

  const first = new Event("vite:preloadError", { cancelable: true });
  dispatch(first);
  expect(first.defaultPrevented).toBe(true);
  expect(values.get(preloadReloadKey)).toBe(target.location.href);
  expect(reload).toHaveBeenCalledOnce();

  const repeated = new Event("vite:preloadError", { cancelable: true });
  dispatch(repeated);
  expect(repeated.defaultPrevented).toBe(false);
  expect(reload).toHaveBeenCalledOnce();
});

it("clears the retry marker after the route renders", () => {
  const { target, values } = recoveryWindow();
  values.set(preloadReloadKey, target.location.href);

  clearPreloadRecovery(target);

  expect(values.has(preloadReloadKey)).toBe(false);
});

it("re-arms after a successful render, not before", () => {
  const { target, values, reload, dispatch } = recoveryWindow();
  installPreloadRecovery(target);

  const first = new Event("vite:preloadError", { cancelable: true });
  dispatch(first);
  expect(reload).toHaveBeenCalledOnce();

  const stillLoading = new Event("vite:preloadError", { cancelable: true });
  dispatch(stillLoading);
  expect(stillLoading.defaultPrevented).toBe(false);
  expect(reload).toHaveBeenCalledOnce();
  expect(values.get(preloadReloadKey)).toBe(target.location.href);

  clearPreloadRecovery(target);

  const afterRender = new Event("vite:preloadError", { cancelable: true });
  dispatch(afterRender);
  expect(afterRender.defaultPrevented).toBe(true);
  expect(reload).toHaveBeenCalledTimes(2);
});
