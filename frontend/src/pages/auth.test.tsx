import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AuthPage, safeNext } from "./auth";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("registration", () => {
  it("lets the first account submit without an invite", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        Response.json({
          authenticated: false,
          is_admin: false,
          overview_public: false,
          version: "dev",
        }),
      ),
    );

    render(
      <MemoryRouter>
        <AuthPage mode="register" />
      </MemoryRouter>,
    );

    expect(await screen.findByLabelText("Invite token")).not.toBeRequired();
    expect(
      screen.getByText(/first account on a new instance needs no invitation/i),
    ).toBeInTheDocument();
  });
});

describe("safeNext", () => {
  it("keeps same-origin application paths", () => {
    expect(safeNext("/sessions/42?range=30d#message-3")).toBe(
      "/sessions/42?range=30d#message-3",
    );
  });

  it.each([
    "//evil.example",
    "/\\evil.example",
    "/%5cevil.example",
  ])("rejects ambiguous external target %s", (target) => {
    expect(safeNext(target)).toBe("/overview");
  });
});
