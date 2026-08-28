package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const authSourceBot = "bot"

// Bot is a shared passwordless account with its API tokens.
type Bot struct {
	ID        int64
	Username  string
	CreatedAt time.Time
	Tokens    []APIToken
}

// CreateBot creates a shared passwordless account when creatorID is a user.
func (s *Store) CreateBot(ctx context.Context, creatorID int64, username string) (Bot, error) {
	var allowed bool
	if err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM users
		    WHERE id = $1 AND auth_source <> 'bot' AND deleted_at IS NULL
		 )`, creatorID).Scan(&allowed); err != nil {
		return Bot{}, fmt.Errorf("check bot creator: %w", err)
	}
	if !allowed {
		return Bot{}, ErrNotFound
	}

	var bot Bot
	err := s.Pool.QueryRow(ctx,
		`UPDATE users SET deleted_at = NULL
		  WHERE username = $1 AND auth_source = 'bot' AND deleted_at IS NOT NULL
		  RETURNING id, username, created_at`, username).
		Scan(&bot.ID, &bot.Username, &bot.CreatedAt)
	if err == nil {
		bot.Tokens = []APIToken{}
		return bot, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Bot{}, fmt.Errorf("restore bot: %w", err)
	}

	err = s.Pool.QueryRow(ctx,
		`INSERT INTO users (username, auth_source)
		 VALUES ($1, 'bot')
		 RETURNING id, username, created_at`,
		username).Scan(&bot.ID, &bot.Username, &bot.CreatedAt)
	bot.Tokens = []APIToken{}
	return bot, err
}

// ListBots returns every shared bot, including its token metadata.
func (s *Store) ListBots(ctx context.Context) ([]Bot, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, username, created_at
		   FROM users
		  WHERE auth_source = 'bot' AND deleted_at IS NULL
		  ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query bots: %w", err)
	}
	defer rows.Close()

	bots := []Bot{}
	byID := map[int64]int{}
	for rows.Next() {
		var bot Bot
		if err := rows.Scan(&bot.ID, &bot.Username, &bot.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan bot: %w", err)
		}
		bot.Tokens = []APIToken{}
		byID[bot.ID] = len(bots)
		bots = append(bots, bot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bots: %w", err)
	}
	if len(bots) == 0 {
		return bots, nil
	}

	tokenRows, err := s.Pool.Query(ctx,
		`SELECT t.id, t.user_id, t.name, t.scope, t.created_at, t.last_used_at, t.revoked_at
		   FROM api_tokens t
		   JOIN users b ON b.id = t.user_id
		  WHERE b.auth_source = 'bot' AND b.deleted_at IS NULL
		  ORDER BY t.created_at DESC, t.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query bot tokens: %w", err)
	}
	defer tokenRows.Close()
	for tokenRows.Next() {
		var token APIToken
		if err := tokenRows.Scan(&token.ID, &token.UserID, &token.Name, &token.Scope,
			&token.CreatedAt, &token.LastUsedAt, &token.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan bot token: %w", err)
		}
		if index, ok := byID[token.UserID]; ok {
			bots[index].Tokens = append(bots[index].Tokens, token)
		}
	}
	if err := tokenRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bot tokens: %w", err)
	}
	return bots, nil
}

// CreateBotAPIToken stores a token for a shared bot.
func (s *Store) CreateBotAPIToken(ctx context.Context, botID int64, name, scope, tokenHash string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO api_tokens (user_id, name, scope, token_hash)
		 SELECT id, $2, $3, $4 FROM users
		  WHERE id = $1 AND auth_source = 'bot' AND deleted_at IS NULL
		 RETURNING id`, botID, name, scope, tokenHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// RevokeBotAPIToken revokes one token on a shared bot.
func (s *Store) RevokeBotAPIToken(ctx context.Context, botID, tokenID int64) error {
	var exists bool
	if err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM users
		    WHERE id = $1 AND auth_source = 'bot' AND deleted_at IS NULL
		 )`, botID).Scan(&exists); err != nil {
		return fmt.Errorf("check bot: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	if _, err := s.Pool.Exec(ctx,
		`UPDATE api_tokens SET revoked_at = now()
		  WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, tokenID, botID); err != nil {
		return fmt.Errorf("revoke bot token: %w", err)
	}
	return nil
}

// DeleteBot hides a shared bot and revokes its credentials without removing its
// sessions. Creating the same username later restores this identity.
func (s *Store) DeleteBot(ctx context.Context, botID int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin bot deletion: %w", err)
	}
	defer tx.Rollback(ctx)

	result, err := tx.Exec(ctx,
		`UPDATE users SET deleted_at = now(), overview_public = FALSE
		  WHERE id = $1 AND auth_source = 'bot' AND deleted_at IS NULL`, botID)
	if err != nil {
		return fmt.Errorf("soft-delete bot: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx,
		`UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, now())
		  WHERE user_id = $1`, botID); err != nil {
		return fmt.Errorf("revoke bot api tokens: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE oauth_tokens SET revoked_at = COALESCE(revoked_at, now())
		  WHERE user_id = $1`, botID); err != nil {
		return fmt.Errorf("revoke bot oauth tokens: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM oauth_auth_codes WHERE user_id = $1`, botID); err != nil {
		return fmt.Errorf("delete bot oauth codes: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM web_sessions WHERE user_id = $1`, botID); err != nil {
		return fmt.Errorf("delete bot web sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit bot deletion: %w", err)
	}
	return nil
}
