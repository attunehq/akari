import { CopyIcon, TrashIcon } from "@phosphor-icons/react";
import { useState } from "react";

import { RequestError, request } from "../api";
import { attempt, notify } from "../components/notices";
import { formatTime } from "../format";
import type { AccountBot, CreatedTokenResponse, Token } from "../types";

export function BotSection({
  bots,
  refresh,
}: {
  bots: AccountBot[];
  refresh: () => void;
}) {
  const [username, setUsername] = useState("");
  return (
    <section className="settings-section">
      <div className="settings-copy">
        <h2>Bot accounts</h2>
        <p>
          Shared identities for CI and other automation. Every user can manage
          them, but bots cannot log in.
        </p>
      </div>
      <div className="settings-control">
        <form
          className="inline-form"
          onSubmit={async (event) => {
            event.preventDefault();
            try {
              await request("/api/v1/app/account/bots", {
                method: "POST",
                body: JSON.stringify({ username }),
              });
              setUsername("");
              notify("Bot created", "ok");
              refresh();
            } catch (error) {
              notify(
                error instanceof RequestError
                  ? error.message
                  : "Could not create the bot.",
                "err",
              );
            }
          }}
        >
          <input
            required
            placeholder="Bot username"
            value={username}
            onChange={(event) => setUsername(event.target.value)}
          />
          <button className="button" type="submit" disabled={!username.trim()}>
            Create bot
          </button>
        </form>
        <div className="settings-list bot-list">
          {bots.length === 0 ? (
            <p className="empty-inline">No bot accounts.</p>
          ) : (
            bots.map((bot) => (
              <BotCard key={bot.id} bot={bot} refresh={refresh} />
            ))
          )}
        </div>
      </div>
    </section>
  );
}

function BotCard({ bot, refresh }: { bot: AccountBot; refresh: () => void }) {
  const [name, setName] = useState("");
  const [secret, setSecret] = useState("");
  return (
    <div className="bot-card">
      <div className="bot-card-head">
        <div>
          <strong>{bot.username}</strong>
          <span>bot · created {formatTime(bot.created_at)}</span>
        </div>
        <button
          type="button"
          className="icon-link danger"
          aria-label={`Delete ${bot.username}`}
          onClick={async () => {
            if (
              !window.confirm(
                `Delete ${bot.username} and revoke all of its tokens? Its sessions will remain.`,
              )
            )
              return;
            if (
              await attempt(
                request(`/api/v1/app/account/bots/${bot.id}`, {
                  method: "DELETE",
                }),
                "Bot deleted",
              )
            )
              refresh();
          }}
        >
          <TrashIcon />
        </button>
      </div>
      <form
        className="inline-form bot-token-form"
        onSubmit={async (event) => {
          event.preventDefault();
          const form = event.currentTarget;
          const data = new FormData(form);
          try {
            const result = await request<CreatedTokenResponse>(
              `/api/v1/app/account/bots/${bot.id}/tokens`,
              {
                method: "POST",
                body: JSON.stringify({
                  name: data.get("name"),
                  scope: data.get("scope"),
                }),
              },
            );
            setSecret(result.token);
            setName("");
            notify("Bot token created", "ok");
            refresh();
          } catch (error) {
            notify(
              error instanceof RequestError
                ? error.message
                : "Could not create the bot token.",
              "err",
            );
          }
        }}
      >
        <input
          name="name"
          required
          placeholder="Token name"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
        <select name="scope" defaultValue="ingest">
          <option value="ingest">Ingest</option>
          <option value="read">Read</option>
          <option value="full">Full</option>
        </select>
        <button
          className="button secondary"
          type="submit"
          disabled={!name.trim()}
        >
          Create token
        </button>
      </form>
      {secret ? (
        <div className="secret">
          <code>{secret}</code>
          <button
            type="button"
            className="icon-link"
            aria-label={`Copy token for ${bot.username}`}
            onClick={() => navigator.clipboard.writeText(secret)}
          >
            <CopyIcon />
          </button>
          <p>Copy this token now. Akari stores only its hash.</p>
        </div>
      ) : null}
      <BotTokens botID={bot.id} tokens={bot.tokens} refresh={refresh} />
    </div>
  );
}

function BotTokens({
  botID,
  tokens,
  refresh,
}: {
  botID: number;
  tokens: Token[];
  refresh: () => void;
}) {
  if (tokens.length === 0)
    return <p className="empty-inline bot-token-empty">No API tokens.</p>;
  return (
    <div className="settings-list bot-token-list">
      {tokens.map((token) => (
        <div
          className={`settings-row${token.RevokedAt ? " revoked" : ""}`}
          key={token.ID}
        >
          <div>
            <strong>{token.Name}</strong>
            <span>
              {token.Scope} · created {formatTime(token.CreatedAt)}
            </span>
          </div>
          {token.RevokedAt ? (
            <span className="tag">revoked</span>
          ) : (
            <button
              type="button"
              className="icon-link danger"
              aria-label={`Revoke ${token.Name}`}
              onClick={async () => {
                if (
                  await attempt(
                    request(
                      `/api/v1/app/account/bots/${botID}/tokens/${token.ID}`,
                      { method: "DELETE" },
                    ),
                    "Bot token revoked",
                  )
                )
                  refresh();
              }}
            >
              <TrashIcon />
            </button>
          )}
        </div>
      ))}
    </div>
  );
}
