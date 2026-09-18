package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"meilirize/internal/mailbox"

	modernsqlite "modernc.org/sqlite"
)

const sqliteConstraintCode = 19

var _ mailbox.Repository = (*Store)(nil)

type rowScanner interface {
	Scan(...any) error
}

func (store *Store) CreateUser(ctx context.Context, params mailbox.CreateUserParams) (mailbox.User, error) {
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return mailbox.User{}, fmt.Errorf("begin user transaction: %w", err)
	}
	defer transaction.Rollback()

	result, err := transaction.ExecContext(
		ctx,
		"INSERT INTO users (display_name) VALUES (?)",
		strings.TrimSpace(params.DisplayName),
	)
	if err != nil {
		return mailbox.User{}, mapWriteError("create user", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return mailbox.User{}, fmt.Errorf("read created user ID: %w", err)
	}
	user, err := userByID(ctx, transaction, id)
	if err != nil {
		return mailbox.User{}, err
	}
	if err := transaction.Commit(); err != nil {
		return mailbox.User{}, fmt.Errorf("commit user transaction: %w", err)
	}
	return user, nil
}

func (store *Store) User(ctx context.Context, id int64) (mailbox.User, error) {
	if id <= 0 {
		return mailbox.User{}, fmt.Errorf("%w: user ID must be positive", mailbox.ErrInvalid)
	}
	return userByID(ctx, store.database, id)
}

func (store *Store) CreateAddress(ctx context.Context, params mailbox.CreateAddressParams) (mailbox.Address, error) {
	if params.OwnerUserID <= 0 {
		return mailbox.Address{}, fmt.Errorf("%w: owner user ID must be positive", mailbox.ErrInvalid)
	}
	address, err := mailbox.NormalizeAddress(params.Address)
	if err != nil {
		return mailbox.Address{}, err
	}
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return mailbox.Address{}, fmt.Errorf("begin address transaction: %w", err)
	}
	defer transaction.Rollback()

	result, err := transaction.ExecContext(
		ctx,
		"INSERT INTO addresses (owner_user_id, address, display_name) VALUES (?, ?, ?)",
		params.OwnerUserID,
		address,
		strings.TrimSpace(params.DisplayName),
	)
	if err != nil {
		return mailbox.Address{}, mapWriteError("create address", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return mailbox.Address{}, fmt.Errorf("read created address ID: %w", err)
	}
	createdAddress, err := addressByID(ctx, transaction, id)
	if err != nil {
		return mailbox.Address{}, err
	}
	if err := transaction.Commit(); err != nil {
		return mailbox.Address{}, fmt.Errorf("commit address transaction: %w", err)
	}
	return createdAddress, nil
}

func (store *Store) Address(ctx context.Context, id int64) (mailbox.Address, error) {
	if id <= 0 {
		return mailbox.Address{}, fmt.Errorf("%w: address ID must be positive", mailbox.ErrInvalid)
	}
	return addressByID(ctx, store.database, id)
}

func (store *Store) AddressByEmail(ctx context.Context, value string) (mailbox.Address, error) {
	address, err := mailbox.NormalizeAddress(value)
	if err != nil {
		return mailbox.Address{}, err
	}
	return scanAddress(store.database.QueryRowContext(
		ctx,
		"SELECT id, owner_user_id, address, display_name, created_at, updated_at FROM addresses WHERE address = ?",
		address,
	))
}

func (store *Store) BindProvider(
	ctx context.Context,
	params mailbox.BindProviderParams,
) (mailbox.ProviderBinding, error) {
	if params.AddressID <= 0 {
		return mailbox.ProviderBinding{}, fmt.Errorf("%w: address ID must be positive", mailbox.ErrInvalid)
	}
	if !params.SendEnabled && !params.ReceiveEnabled {
		return mailbox.ProviderBinding{}, fmt.Errorf(
			"%w: provider must support sending or receiving",
			mailbox.ErrInvalid,
		)
	}
	provider, err := mailbox.NormalizeProvider(params.Provider)
	if err != nil {
		return mailbox.ProviderBinding{}, err
	}
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return mailbox.ProviderBinding{}, fmt.Errorf("begin provider binding transaction: %w", err)
	}
	defer transaction.Rollback()

	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO provider_bindings
            (address_id, provider, send_enabled, receive_enabled)
         VALUES (?, ?, ?, ?)`,
		params.AddressID,
		provider,
		params.SendEnabled,
		params.ReceiveEnabled,
	)
	if err != nil {
		return mailbox.ProviderBinding{}, mapWriteError("bind provider", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return mailbox.ProviderBinding{}, fmt.Errorf("read provider binding ID: %w", err)
	}
	binding, err := providerBindingByID(ctx, transaction, id)
	if err != nil {
		return mailbox.ProviderBinding{}, err
	}
	if err := transaction.Commit(); err != nil {
		return mailbox.ProviderBinding{}, fmt.Errorf("commit provider binding transaction: %w", err)
	}
	return binding, nil
}

func (store *Store) ProviderBindings(ctx context.Context, addressID int64) ([]mailbox.ProviderBinding, error) {
	if addressID <= 0 {
		return nil, fmt.Errorf("%w: address ID must be positive", mailbox.ErrInvalid)
	}
	rows, err := store.database.QueryContext(
		ctx,
		`SELECT id, address_id, provider, send_enabled, receive_enabled, created_at, updated_at
         FROM provider_bindings WHERE address_id = ? ORDER BY id`,
		addressID,
	)
	if err != nil {
		return nil, fmt.Errorf("list provider bindings: %w", err)
	}
	defer rows.Close()

	bindings := make([]mailbox.ProviderBinding, 0)
	for rows.Next() {
		binding, err := scanProviderBinding(rows)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider bindings: %w", err)
	}
	return bindings, nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func userByID(ctx context.Context, queries rowQuerier, id int64) (mailbox.User, error) {
	return scanUser(queries.QueryRowContext(
		ctx,
		"SELECT id, display_name, created_at, updated_at FROM users WHERE id = ?",
		id,
	))
}

func addressByID(ctx context.Context, queries rowQuerier, id int64) (mailbox.Address, error) {
	return scanAddress(queries.QueryRowContext(
		ctx,
		"SELECT id, owner_user_id, address, display_name, created_at, updated_at FROM addresses WHERE id = ?",
		id,
	))
}

func providerBindingByID(
	ctx context.Context,
	queries rowQuerier,
	id int64,
) (mailbox.ProviderBinding, error) {
	return scanProviderBinding(queries.QueryRowContext(
		ctx,
		`SELECT id, address_id, provider, send_enabled, receive_enabled, created_at, updated_at
         FROM provider_bindings WHERE id = ?`,
		id,
	))
}

func scanUser(row rowScanner) (mailbox.User, error) {
	var user mailbox.User
	var createdAt string
	var updatedAt string
	if err := row.Scan(&user.ID, &user.DisplayName, &createdAt, &updatedAt); err != nil {
		return mailbox.User{}, mapReadError("read user", err)
	}
	var err error
	user.CreatedAt, err = parseTimestamp(createdAt)
	if err != nil {
		return mailbox.User{}, err
	}
	user.UpdatedAt, err = parseTimestamp(updatedAt)
	if err != nil {
		return mailbox.User{}, err
	}
	return user, nil
}

func scanAddress(row rowScanner) (mailbox.Address, error) {
	var address mailbox.Address
	var createdAt string
	var updatedAt string
	if err := row.Scan(
		&address.ID,
		&address.OwnerUserID,
		&address.Address,
		&address.DisplayName,
		&createdAt,
		&updatedAt,
	); err != nil {
		return mailbox.Address{}, mapReadError("read address", err)
	}
	var err error
	address.CreatedAt, err = parseTimestamp(createdAt)
	if err != nil {
		return mailbox.Address{}, err
	}
	address.UpdatedAt, err = parseTimestamp(updatedAt)
	if err != nil {
		return mailbox.Address{}, err
	}
	return address, nil
}

func scanProviderBinding(row rowScanner) (mailbox.ProviderBinding, error) {
	var binding mailbox.ProviderBinding
	var createdAt string
	var updatedAt string
	if err := row.Scan(
		&binding.ID,
		&binding.AddressID,
		&binding.Provider,
		&binding.SendEnabled,
		&binding.ReceiveEnabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return mailbox.ProviderBinding{}, mapReadError("read provider binding", err)
	}
	var err error
	binding.CreatedAt, err = parseTimestamp(createdAt)
	if err != nil {
		return mailbox.ProviderBinding{}, err
	}
	binding.UpdatedAt, err = parseTimestamp(updatedAt)
	if err != nil {
		return mailbox.ProviderBinding{}, err
	}
	return binding, nil
}

func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse SQLite timestamp %q: %w", value, err)
	}
	return parsed, nil
}

func mapReadError(operation string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, mailbox.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func mapWriteError(operation string, err error) error {
	var sqliteError *modernsqlite.Error
	if errors.As(err, &sqliteError) && sqliteError.Code()&0xff == sqliteConstraintCode {
		return fmt.Errorf("%s: %w", operation, mailbox.ErrConflict)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
