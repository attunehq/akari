import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AccountBot } from "../types";
import { BotSection } from "./account-bots";

const bot: AccountBot = {
  created_at: "2026-08-28T12:00:00Z",
  id: 7,
  tokens: [
    {
      CreatedAt: "2026-08-28T12:01:00Z",
      ID: 9,
      LastUsedAt: null,
      Name: "review workflow",
      RevokedAt: null,
      Scope: "ingest",
    },
  ],
  username: "ci-review",
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("BotSection", () => {
  it("creates a bot account", async () => {
    const refresh = vi.fn();
    const fetchMock = vi.fn(async () =>
      Response.json({ ...bot, tokens: [] }, { status: 201 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    render(<BotSection bots={[]} refresh={refresh} />);
    const create = screen.getByRole("button", { name: "Create bot" });
    expect(create).toBeDisabled();
    fireEvent.change(screen.getByPlaceholderText("Bot username"), {
      target: { value: "ci-review" },
    });
    fireEvent.click(create);

    await vi.waitFor(() => expect(refresh).toHaveBeenCalledOnce());
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/app/account/bots",
      expect.objectContaining({
        body: JSON.stringify({ username: "ci-review" }),
        method: "POST",
      }),
    );
  });

  it("creates and revokes bot tokens", async () => {
    const refresh = vi.fn();
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === "POST") {
          return Response.json(
            { id: 10, name: "code review", scope: "full", token: "secret" },
            { status: 201 },
          );
        }
        return Response.json({ revoked: true });
      },
    );
    vi.stubGlobal("fetch", fetchMock);

    render(<BotSection bots={[bot]} refresh={refresh} />);
    const card = screen.getByText("ci-review").closest(".bot-card");
    expect(card).not.toBeNull();
    const controls = within(card as HTMLElement);
    fireEvent.change(controls.getByPlaceholderText("Token name"), {
      target: { value: "code review" },
    });
    fireEvent.change(controls.getByRole("combobox"), {
      target: { value: "full" },
    });
    fireEvent.click(controls.getByRole("button", { name: "Create token" }));

    expect(await controls.findByText("secret")).toBeInTheDocument();
    fireEvent.click(
      controls.getByRole("button", { name: "Revoke review workflow" }),
    );
    await vi.waitFor(() => expect(refresh).toHaveBeenCalledTimes(2));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/app/account/bots/7/tokens/9",
      expect.objectContaining({ method: "DELETE" }),
    );
  });

  it("confirms that deletion revokes tokens but keeps sessions", async () => {
    const refresh = vi.fn();
    const confirm = vi.fn(() => true);
    const fetchMock = vi.fn(async () => Response.json({ deleted: true }));
    vi.stubGlobal("confirm", confirm);
    vi.stubGlobal("fetch", fetchMock);

    render(<BotSection bots={[bot]} refresh={refresh} />);
    fireEvent.click(screen.getByRole("button", { name: "Delete ci-review" }));

    await vi.waitFor(() => expect(refresh).toHaveBeenCalledOnce());
    expect(confirm).toHaveBeenCalledWith(
      "Delete ci-review and revoke all of its tokens? Its sessions will remain.",
    );
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/app/account/bots/7",
      expect.objectContaining({ method: "DELETE" }),
    );
  });
});
