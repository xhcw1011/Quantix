package data

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a DB query finds no matching row.
var ErrNotFound = errors.New("not found")

// ─── Users ────────────────────────────────────────────────────────────────────

// CreateUser inserts a new user and returns the assigned id.
func (s *Store) CreateUser(ctx context.Context, username, email, passwordHash string) (int, error) {
	const q = `
		INSERT INTO users (username, email, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id`
	var id int
	err := s.pool.QueryRow(ctx, q, username, email, passwordHash).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create user: %w", err)
	}
	return id, nil
}

// GetUserByUsername fetches a user by username; returns ErrNotFound if absent.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	const q = `
		SELECT id, username, email, password_hash, role, is_active, created_at,
		       tg_bot_token, tg_chat_id
		FROM users WHERE username = $1`
	u := &User{}
	err := s.pool.QueryRow(ctx, q, username).Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.IsActive, &u.CreatedAt,
		&u.TgBotToken, &u.TgChatID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

// GetUserByID fetches a user by primary key; returns ErrNotFound if absent.
func (s *Store) GetUserByID(ctx context.Context, id int) (*User, error) {
	const q = `
		SELECT id, username, email, password_hash, role, is_active, created_at,
		       tg_bot_token, tg_chat_id
		FROM users WHERE id = $1`
	u := &User{}
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.IsActive, &u.CreatedAt,
		&u.TgBotToken, &u.TgChatID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

// GetAllUsers returns all users (admin use).
func (s *Store) GetAllUsers(ctx context.Context) ([]User, error) {
	const q = `
		SELECT id, username, email, password_hash, role, is_active, created_at,
		       tg_bot_token, tg_chat_id
		FROM users ORDER BY id`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("get all users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.IsActive, &u.CreatedAt,
			&u.TgBotToken, &u.TgChatID); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// GetUserNotifications returns the stored Telegram bot token and chat ID for a user.
func (s *Store) GetUserNotifications(ctx context.Context, userID int) (tgBotToken string, tgChatID int64, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT tg_bot_token, tg_chat_id FROM users WHERE id = $1`, userID,
	).Scan(&tgBotToken, &tgChatID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	return tgBotToken, tgChatID, err
}

// UpdateUserNotifications persists per-user Telegram settings.
func (s *Store) UpdateUserNotifications(ctx context.Context, userID int, tgBotToken string, tgChatID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET tg_bot_token = $1, tg_chat_id = $2 WHERE id = $3`,
		tgBotToken, tgChatID, userID,
	)
	if err != nil {
		return fmt.Errorf("update notifications: %w", err)
	}
	return nil
}

// GetUserRole returns the role string for the given user ID.
func (s *Store) GetUserRole(ctx context.Context, userID int) (string, error) {
	var role string
	err := s.pool.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get user role: %w", err)
	}
	return role, nil
}

// SetUserActive enables or disables a user account.
func (s *Store) SetUserActive(ctx context.Context, userID int, active bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET is_active = $1 WHERE id = $2`, active, userID)
	if err != nil {
		return fmt.Errorf("set user active: %w", err)
	}
	return nil
}

// CreateAdminUser inserts a new admin-role user and returns the assigned id.
func (s *Store) CreateAdminUser(ctx context.Context, username, email, passwordHash string) (int, error) {
	const q = `
		INSERT INTO users (username, email, password_hash, role)
		VALUES ($1, $2, $3, 'admin')
		RETURNING id`
	var id int
	err := s.pool.QueryRow(ctx, q, username, email, passwordHash).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create admin user: %w", err)
	}
	return id, nil
}

// SetUserRole updates the role of a user.
func (s *Store) SetUserRole(ctx context.Context, userID int, role string) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET role = $1 WHERE id = $2`, role, userID)
	if err != nil {
		return fmt.Errorf("set user role: %w", err)
	}
	return nil
}

// UpdatePassword replaces the password_hash for the given user.
func (s *Store) UpdatePassword(ctx context.Context, userID int, newHash string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET password_hash = $1 WHERE id = $2`, newHash, userID)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return nil
}

// IsUserActive returns true if the user account exists and is_active = true.
func (s *Store) IsUserActive(ctx context.Context, userID int) (bool, error) {
	var active bool
	err := s.pool.QueryRow(ctx,
		`SELECT is_active FROM users WHERE id = $1`, userID,
	).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("is user active: %w", err)
	}
	return active, nil
}


// ─── Exchange Credentials ─────────────────────────────────────────────────────

// CreateCredential inserts a new exchange credential record and returns its id.
func (s *Store) CreateCredential(ctx context.Context, c *Credential) (int, error) {
	const q = `
		INSERT INTO exchange_credentials
			(user_id, exchange, label, api_key, api_secret, passphrase, testnet, demo, market_type)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id`
	var id int
	err := s.pool.QueryRow(ctx, q,
		c.UserID, c.Exchange, c.Label, c.APIKey, c.APISecret,
		c.Passphrase, c.Testnet, c.Demo, c.MarketType,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create credential: %w", err)
	}
	return id, nil
}

// GetCredentials returns all active credentials for a user.
func (s *Store) GetCredentials(ctx context.Context, userID int) ([]Credential, error) {
	const q = `
		SELECT id, user_id, exchange, label, api_key, api_secret, passphrase,
		       testnet, demo, market_type, is_active, created_at
		FROM exchange_credentials
		WHERE user_id = $1
		ORDER BY created_at ASC`
	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()

	var out []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(
			&c.ID, &c.UserID, &c.Exchange, &c.Label, &c.APIKey, &c.APISecret,
			&c.Passphrase, &c.Testnet, &c.Demo, &c.MarketType, &c.IsActive, &c.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCredentialByID returns a credential owned by userID; ErrNotFound if absent.
func (s *Store) GetCredentialByID(ctx context.Context, id, userID int) (*Credential, error) {
	const q = `
		SELECT id, user_id, exchange, label, api_key, api_secret, passphrase,
		       testnet, demo, market_type, is_active, created_at
		FROM exchange_credentials
		WHERE id = $1 AND user_id = $2`
	c := &Credential{}
	err := s.pool.QueryRow(ctx, q, id, userID).Scan(
		&c.ID, &c.UserID, &c.Exchange, &c.Label, &c.APIKey, &c.APISecret,
		&c.Passphrase, &c.Testnet, &c.Demo, &c.MarketType, &c.IsActive, &c.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get credential: %w", err)
	}
	return c, nil
}

// UpdateCredential updates label, api_key, api_secret, passphrase, testnet, demo, market_type.
func (s *Store) UpdateCredential(ctx context.Context, c *Credential) error {
	const q = `
		UPDATE exchange_credentials
		SET label=$1, api_key=$2, api_secret=$3, passphrase=$4,
		    testnet=$5, demo=$6, market_type=$7
		WHERE id=$8 AND user_id=$9`
	tag, err := s.pool.Exec(ctx, q,
		c.Label, c.APIKey, c.APISecret, c.Passphrase,
		c.Testnet, c.Demo, c.MarketType, c.ID, c.UserID,
	)
	if err != nil {
		return fmt.Errorf("update credential: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCredential soft-deletes a credential (sets is_active=false).
func (s *Store) DeleteCredential(ctx context.Context, id, userID int) error {
	const q = `UPDATE exchange_credentials SET is_active=false WHERE id=$1 AND user_id=$2`
	tag, err := s.pool.Exec(ctx, q, id, userID)
	if err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ─── Fills ────────────────────────────────────────────────────────────────────

// InsertFill persists a single trade fill to the database.
func (s *Store) InsertFill(ctx context.Context, f *Fill) error {
	const q = `
		INSERT INTO fills
			(user_id, strategy_id, symbol, side, position_side, qty, price, fee, realized_pnl,
			 exchange_order_id, mode, filled_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`
	filledAt := f.FilledAt
	if filledAt.IsZero() {
		filledAt = time.Now()
	}
	_, err := s.pool.Exec(ctx, q,
		f.UserID, f.StrategyID, f.Symbol, f.Side, f.PositionSide,
		f.Qty, f.Price, f.Fee, f.RealizedPnL,
		f.ExchangeOrderID, f.Mode, filledAt,
	)
	if err != nil {
		return fmt.Errorf("insert fill: %w", err)
	}
	return nil
}

// RecordFilter restricts fill/order queries. Zero values are ignored.
type RecordFilter struct {
	Symbol        string
	StrategyID    string
	Mode          string    // "live" | "paper"
	From          time.Time // zero → no lower bound
	To            time.Time // zero → no upper bound
	ClientOrderID string    // exact match on client_order_id (orders only)
}

// GetFills returns paginated fills for a user, newest first, with optional filtering.
func (s *Store) GetFills(ctx context.Context, userID, limit, offset int, f RecordFilter) ([]Fill, error) {
	q := `
		SELECT id, user_id, strategy_id, symbol, side, COALESCE(position_side,''), qty, price, fee,
		       realized_pnl, exchange_order_id, mode, filled_at
		FROM fills
		WHERE user_id = $1`
	args := []any{userID}

	if f.Symbol != "" {
		args = append(args, f.Symbol)
		q += fmt.Sprintf(" AND symbol = $%d", len(args))
	}
	if f.StrategyID != "" {
		args = append(args, f.StrategyID)
		q += fmt.Sprintf(" AND strategy_id = $%d", len(args))
	}
	if f.Mode != "" {
		args = append(args, f.Mode)
		q += fmt.Sprintf(" AND mode = $%d", len(args))
	}
	if !f.From.IsZero() {
		args = append(args, f.From)
		q += fmt.Sprintf(" AND filled_at >= $%d", len(args))
	}
	if !f.To.IsZero() {
		args = append(args, f.To)
		q += fmt.Sprintf(" AND filled_at <= $%d", len(args))
	}

	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY filled_at DESC LIMIT $%d", len(args))
	args = append(args, offset)
	q += fmt.Sprintf(" OFFSET $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("get fills: %w", err)
	}
	defer rows.Close()

	var out []Fill
	for rows.Next() {
		var f Fill
		if err := rows.Scan(
			&f.ID, &f.UserID, &f.StrategyID, &f.Symbol, &f.Side, &f.PositionSide,
			&f.Qty, &f.Price, &f.Fee, &f.RealizedPnL,
			&f.ExchangeOrderID, &f.Mode, &f.FilledAt,
		); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetAllFillsForStrategy returns all fills for a user+strategy+mode in chronological
// order (oldest first). Used by paper engine at startup to reconstruct PositionManager.
func (s *Store) GetAllFillsForStrategy(ctx context.Context, userID int, strategyID, mode string) ([]Fill, error) {
	const q = `
		SELECT id, user_id, strategy_id, symbol, side, COALESCE(position_side,''), qty, price, fee,
		       realized_pnl, exchange_order_id, mode, filled_at
		FROM fills
		WHERE user_id = $1 AND strategy_id = $2 AND mode = $3
		ORDER BY filled_at ASC`
	rows, err := s.pool.Query(ctx, q, userID, strategyID, mode)
	if err != nil {
		return nil, fmt.Errorf("get all fills for strategy: %w", err)
	}
	defer rows.Close()

	var out []Fill
	for rows.Next() {
		var f Fill
		if err := rows.Scan(
			&f.ID, &f.UserID, &f.StrategyID, &f.Symbol, &f.Side, &f.PositionSide,
			&f.Qty, &f.Price, &f.Fee, &f.RealizedPnL,
			&f.ExchangeOrderID, &f.Mode, &f.FilledAt,
		); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ─── Equity Snapshots ─────────────────────────────────────────────────────────

// InsertEquitySnapshot saves a point-in-time equity record.
func (s *Store) InsertEquitySnapshot(ctx context.Context, e *EquitySnapshot) error {
	const q = `
		INSERT INTO equity_snapshots
			(user_id, strategy_id, equity, cash, unrealized_pnl, realized_pnl, snapshotted_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`
	at := e.SnapshottedAt
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.pool.Exec(ctx, q,
		e.UserID, e.StrategyID, e.Equity, e.Cash,
		e.UnrealizedPnL, e.RealizedPnL, at,
	)
	if err != nil {
		return fmt.Errorf("insert equity snapshot: %w", err)
	}
	return nil
}

// GetEquitySnapshots returns the most recent equity snapshots for a user/strategy.
func (s *Store) GetEquitySnapshots(ctx context.Context, userID int, strategyID string, limit int) ([]EquitySnapshot, error) {
	q := `
		SELECT id, user_id, strategy_id, equity, cash, unrealized_pnl, realized_pnl, snapshotted_at
		FROM equity_snapshots
		WHERE user_id = $1`
	args := []any{userID}
	if strategyID != "" {
		args = append(args, strategyID)
		q += fmt.Sprintf(" AND strategy_id = $%d", len(args))
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY snapshotted_at ASC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("get equity snapshots: %w", err)
	}
	defer rows.Close()

	var out []EquitySnapshot
	for rows.Next() {
		var e EquitySnapshot
		if err := rows.Scan(
			&e.ID, &e.UserID, &e.StrategyID, &e.Equity, &e.Cash,
			&e.UnrealizedPnL, &e.RealizedPnL, &e.SnapshottedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetLatestEquitySnapshot returns the single most-recent equity snapshot for a
// user+strategy. Returns nil, nil when no snapshot exists yet.
// Used by the paper engine at startup to restore cash/equity.
func (s *Store) GetLatestEquitySnapshot(ctx context.Context, userID int, strategyID string) (*EquitySnapshot, error) {
	const q = `
		SELECT id, user_id, strategy_id, equity, cash, unrealized_pnl, realized_pnl, snapshotted_at
		FROM equity_snapshots
		WHERE user_id = $1 AND strategy_id = $2
		ORDER BY snapshotted_at DESC LIMIT 1`
	e := &EquitySnapshot{}
	err := s.pool.QueryRow(ctx, q, userID, strategyID).Scan(
		&e.ID, &e.UserID, &e.StrategyID, &e.Equity, &e.Cash,
		&e.UnrealizedPnL, &e.RealizedPnL, &e.SnapshottedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get latest equity snapshot: %w", err)
	}
	return e, nil
}

// ─── Orders ───────────────────────────────────────────────────────────────────

// GetOrders returns paginated orders for a user, newest first, with optional filtering.
func (s *Store) GetOrders(ctx context.Context, userID, limit, offset int, f RecordFilter) ([]OrderRecord, error) {
	q := `
		SELECT id, COALESCE(exchange_id,''), symbol, side, type, status,
		       quantity, COALESCE(price,0), filled_quantity,
		       COALESCE(avg_fill_price,0), commission,
		       COALESCE(strategy_id,''), mode,
		       COALESCE(user_id,0), COALESCE(credential_id,0), created_at, updated_at,
		       COALESCE(position_side,''), COALESCE(stop_price,0),
		       COALESCE(reject_reason,''), COALESCE(client_order_id,''),
		       COALESCE(order_role,'')
		FROM orders
		WHERE user_id = $1`
	args := []any{userID}

	if f.Symbol != "" {
		args = append(args, f.Symbol)
		q += fmt.Sprintf(" AND symbol = $%d", len(args))
	}
	if f.StrategyID != "" {
		args = append(args, f.StrategyID)
		q += fmt.Sprintf(" AND strategy_id = $%d", len(args))
	}
	if f.Mode != "" {
		args = append(args, f.Mode)
		q += fmt.Sprintf(" AND mode = $%d", len(args))
	}
	if !f.From.IsZero() {
		args = append(args, f.From)
		q += fmt.Sprintf(" AND created_at >= $%d", len(args))
	}
	if !f.To.IsZero() {
		args = append(args, f.To)
		q += fmt.Sprintf(" AND created_at <= $%d", len(args))
	}
	if f.ClientOrderID != "" {
		args = append(args, f.ClientOrderID)
		q += fmt.Sprintf(" AND client_order_id = $%d", len(args))
	}

	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	args = append(args, offset)
	q += fmt.Sprintf(" OFFSET $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("get orders: %w", err)
	}
	defer rows.Close()

	var out []OrderRecord
	for rows.Next() {
		var o OrderRecord
		if err := rows.Scan(
			&o.ID, &o.ExchangeID, &o.Symbol, &o.Side, &o.Type, &o.Status,
			&o.Quantity, &o.Price, &o.FilledQuantity, &o.AvgFillPrice,
			&o.Commission, &o.StrategyID, &o.Mode, &o.UserID, &o.CredentialID,
			&o.CreatedAt, &o.UpdatedAt,
			&o.PositionSide, &o.StopPrice, &o.RejectReason, &o.ClientOrderID,
			&o.OrderRole,
		); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// UpsertOrder inserts or updates an order record using client_order_id as the idempotency key.
// If client_order_id is empty the row is always inserted (no conflict check possible).
func (s *Store) UpsertOrder(ctx context.Context, o *OrderRecord) error {
	const q = `
		INSERT INTO orders (
			exchange_id, symbol, side, position_side, type, status,
			quantity, price, stop_price, filled_quantity, avg_fill_price, commission,
			reject_reason, strategy_id, mode, user_id, credential_id,
			client_order_id, order_role, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,
			$7,$8,$9,$10,$11,$12,
			$13,$14,$15,$16,$17,
			$18,$19,$20,NOW()
		)
		ON CONFLICT (client_order_id) WHERE client_order_id IS NOT NULL
		DO UPDATE SET
			exchange_id     = EXCLUDED.exchange_id,
			status          = EXCLUDED.status,
			filled_quantity = EXCLUDED.filled_quantity,
			avg_fill_price  = EXCLUDED.avg_fill_price,
			commission      = EXCLUDED.commission,
			reject_reason   = EXCLUDED.reject_reason,
			order_role      = EXCLUDED.order_role,
			updated_at      = NOW()`

	createdAt := o.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	_, err := s.pool.Exec(ctx, q,
		o.ExchangeID, o.Symbol, o.Side, o.PositionSide, o.Type, o.Status,
		o.Quantity, o.Price, o.StopPrice, o.FilledQuantity, o.AvgFillPrice, o.Commission,
		o.RejectReason, o.StrategyID, o.Mode, o.UserID, o.CredentialID,
		o.ClientOrderID, o.OrderRole, createdAt,
	)
	if err != nil {
		return fmt.Errorf("upsert order: %w", err)
	}
	return nil
}

// GetActiveOrders returns PENDING, OPEN, and PARTIAL orders for a user+strategy.
// Used by the live engine at startup to recover OMS state from the database.
func (s *Store) GetActiveOrders(ctx context.Context, userID int, strategyID string) ([]*OrderRecord, error) {
	const q = `
		SELECT id, COALESCE(exchange_id,''), symbol, side, type, status,
		       quantity, COALESCE(price,0), filled_quantity,
		       COALESCE(avg_fill_price,0), commission,
		       COALESCE(strategy_id,''), mode,
		       COALESCE(user_id,0), COALESCE(credential_id,0), created_at, updated_at,
		       COALESCE(position_side,''), COALESCE(stop_price,0),
		       COALESCE(reject_reason,''), COALESCE(client_order_id,''),
		       COALESCE(order_role,'')
		FROM orders
		WHERE user_id = $1 AND strategy_id = $2
		  AND status IN ('PENDING', 'OPEN', 'PARTIAL')
		ORDER BY created_at ASC`

	rows, err := s.pool.Query(ctx, q, userID, strategyID)
	if err != nil {
		return nil, fmt.Errorf("get active orders: %w", err)
	}
	defer rows.Close()

	var out []*OrderRecord
	for rows.Next() {
		o := &OrderRecord{}
		if err := rows.Scan(
			&o.ID, &o.ExchangeID, &o.Symbol, &o.Side, &o.Type, &o.Status,
			&o.Quantity, &o.Price, &o.FilledQuantity, &o.AvgFillPrice,
			&o.Commission, &o.StrategyID, &o.Mode, &o.UserID, &o.CredentialID,
			&o.CreatedAt, &o.UpdatedAt,
			&o.PositionSide, &o.StopPrice, &o.RejectReason, &o.ClientOrderID,
			&o.OrderRole,
		); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CancelOrderByID updates a single order to CANCELLED by its primary key ID.
// Used during recovery when a specific order (not all) must be marked cancelled.
func (s *Store) CancelOrderByID(ctx context.Context, orderID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE orders
		SET status = 'CANCELLED', updated_at = NOW()
		WHERE id = $1
		  AND status IN ('PENDING', 'OPEN', 'PARTIAL')`,
		orderID,
	)
	if err != nil {
		return fmt.Errorf("cancel order by id: %w", err)
	}
	return nil
}

// CancelActiveOrders bulk-updates all PENDING/OPEN/PARTIAL orders for a user+strategy
// to CANCELLED. Called after engine startup clean-slate to sync DB state.
func (s *Store) CancelActiveOrders(ctx context.Context, userID int, strategyID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE orders
		SET status = 'CANCELLED', updated_at = NOW()
		WHERE user_id = $1 AND strategy_id = $2
		  AND status IN ('PENDING', 'OPEN', 'PARTIAL')`,
		userID, strategyID,
	)
	if err != nil {
		return fmt.Errorf("cancel active orders: %w", err)
	}
	return nil
}

// ─── Engine Sessions ──────────────────────────────────────────────────────────

// UpsertEngineSession records (or updates) a running engine session.
// Called when an engine starts; engineID is "{symbol}-{interval}-{strategyID}".
func (s *Store) UpsertEngineSession(ctx context.Context, userID int, engineID string, reqJSON []byte) error {
	const q = `
		INSERT INTO engine_sessions (user_id, engine_id, request_json, is_active, started_at)
		VALUES ($1, $2, $3, TRUE, NOW())
		ON CONFLICT (user_id, engine_id) DO UPDATE SET
			request_json = EXCLUDED.request_json,
			is_active    = TRUE,
			started_at   = NOW(),
			stopped_at   = NULL`
	_, err := s.pool.Exec(ctx, q, userID, engineID, reqJSON)
	if err != nil {
		return fmt.Errorf("upsert engine session: %w", err)
	}
	return nil
}

// DeactivateEngineSession marks a session as stopped (is_active = false).
// Called when an engine stops normally or encounters a fatal error.
func (s *Store) DeactivateEngineSession(ctx context.Context, userID int, engineID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE engine_sessions
		SET is_active = FALSE, stopped_at = NOW()
		WHERE user_id = $1 AND engine_id = $2`,
		userID, engineID,
	)
	if err != nil {
		return fmt.Errorf("deactivate engine session: %w", err)
	}
	return nil
}

// GetActiveEngineSessions returns all sessions where is_active = true.
// Used by AutoRestart to replay engine starts after a server restart.
func (s *Store) GetActiveEngineSessions(ctx context.Context) ([]EngineSessionRow, error) {
	const q = `
		SELECT user_id, engine_id, request_json
		FROM engine_sessions
		WHERE is_active = TRUE
		ORDER BY started_at ASC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("get active engine sessions: %w", err)
	}
	defer rows.Close()

	var out []EngineSessionRow
	for rows.Next() {
		var r EngineSessionRow
		if err := rows.Scan(&r.UserID, &r.EngineID, &r.RequestJSON); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─── Strategy Positions (DB backup for Redis-primary position cache) ─────────

// StrategyPositionRecord is the DB representation of a strategy position.
type StrategyPositionRecord struct {
	UserID     int
	EngineID   string
	Side       string
	Symbol     string
	Mode       string
	Qty        float64
	EntryPrice float64
	StopLoss   float64
	TakeProfit float64
	Trailing   float64
	PeakPrice  float64
	RValue     float64
	InitQty    float64
	TP1Hit     bool
	BarsHeld   int
	OrderID    string
	Filled     bool
}

// UpsertStrategyPosition inserts or updates a strategy position record.
func (s *Store) UpsertStrategyPosition(ctx context.Context, userID int, engineID string, rec *StrategyPositionRecord) error {
	const q = `
		INSERT INTO strategy_positions (
			user_id, engine_id, side, symbol, mode, qty, entry_price,
			stop_loss, take_profit, "trailing", peak_price, r_value,
			init_qty, tp1_hit, bars_held, order_id, filled, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,NOW())
		ON CONFLICT (user_id, engine_id, side) DO UPDATE SET
			symbol=EXCLUDED.symbol, mode=EXCLUDED.mode, qty=EXCLUDED.qty,
			entry_price=EXCLUDED.entry_price, stop_loss=EXCLUDED.stop_loss,
			take_profit=EXCLUDED.take_profit, "trailing"=EXCLUDED."trailing",
			peak_price=EXCLUDED.peak_price, r_value=EXCLUDED.r_value,
			init_qty=EXCLUDED.init_qty, tp1_hit=EXCLUDED.tp1_hit,
			bars_held=EXCLUDED.bars_held, order_id=EXCLUDED.order_id,
			filled=EXCLUDED.filled, updated_at=NOW()`

	_, err := s.pool.Exec(ctx, q,
		userID, engineID, rec.Side, rec.Symbol, rec.Mode, rec.Qty, rec.EntryPrice,
		rec.StopLoss, rec.TakeProfit, rec.Trailing, rec.PeakPrice, rec.RValue,
		rec.InitQty, rec.TP1Hit, rec.BarsHeld, rec.OrderID, rec.Filled,
	)
	if err != nil {
		return fmt.Errorf("upsert strategy position: %w", err)
	}
	return nil
}

// DeleteStrategyPosition removes a strategy position record.
func (s *Store) DeleteStrategyPosition(ctx context.Context, userID int, engineID, side string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM strategy_positions WHERE user_id=$1 AND engine_id=$2 AND side=$3`,
		userID, engineID, side,
	)
	if err != nil {
		return fmt.Errorf("delete strategy position: %w", err)
	}
	return nil
}
