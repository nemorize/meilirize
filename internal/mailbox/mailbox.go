package mailbox

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

var (
	ErrConflict = errors.New("mailbox record conflicts with existing data")
	ErrInvalid  = errors.New("invalid mailbox data")
	ErrNotFound = errors.New("mailbox record not found")
)

type User struct {
	ID          int64
	DisplayName string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Address struct {
	ID          int64
	OwnerUserID int64
	Address     string
	DisplayName string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type ProviderBinding struct {
	ID             int64
	AddressID      int64
	Provider       string
	SendEnabled    bool
	ReceiveEnabled bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreateUserParams struct {
	DisplayName string
}

type CreateAddressParams struct {
	OwnerUserID int64
	Address     string
	DisplayName string
}

type BindProviderParams struct {
	AddressID      int64
	Provider       string
	SendEnabled    bool
	ReceiveEnabled bool
}

type IdentityRepository interface {
	CreateUser(context.Context, CreateUserParams) (User, error)
	User(context.Context, int64) (User, error)
	CreateAddress(context.Context, CreateAddressParams) (Address, error)
	Address(context.Context, int64) (Address, error)
	AddressByEmail(context.Context, string) (Address, error)
	BindProvider(context.Context, BindProviderParams) (ProviderBinding, error)
	ProviderBindings(context.Context, int64) ([]ProviderBinding, error)
}

type Repository interface {
	IdentityRepository
	MessageRepository
}

func NormalizeAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" || parsed.Address != value {
		return "", fmt.Errorf("%w: invalid email address %q", ErrInvalid, value)
	}

	separator := strings.LastIndexByte(value, '@')
	if separator <= 0 || separator == len(value)-1 {
		return "", fmt.Errorf("%w: invalid email address %q", ErrInvalid, value)
	}
	return value[:separator+1] + strings.ToLower(value[separator+1:]), nil
}

func NormalizeProvider(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", fmt.Errorf("%w: provider must not be empty", ErrInvalid)
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return "", fmt.Errorf("%w: provider must not contain whitespace", ErrInvalid)
	}
	return value, nil
}
