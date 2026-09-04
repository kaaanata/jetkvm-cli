// Package credentials resolves JetKVM credentials from explicit local sources.
package credentials

import (
	"context"
	"errors"
	"os"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
	"github.com/zalando/go-keyring"
)

var ErrUnavailable = errors.New("credential is unavailable")

// New builds the configured credential provider. No-password devices return
// nil because the HTTP client must not attempt a login for them.
func New(cfg config.CredentialConfig) (jetkvm.CredentialProvider, error) {
	switch cfg.Provider {
	case config.CredentialNoPassword:
		return nil, nil
	case config.CredentialEnvironment:
		return environmentProvider{name: cfg.Variable}, nil
	case config.CredentialKeychain:
		return keychainProvider{service: cfg.Service, account: cfg.Account}, nil
	default:
		return nil, ErrUnavailable
	}
}

type environmentProvider struct{ name string }

func (p environmentProvider) Password(context.Context) ([]byte, error) {
	value, ok := os.LookupEnv(p.name)
	if !ok || value == "" {
		return nil, ErrUnavailable
	}
	return []byte(value), nil
}

type keychainProvider struct {
	service string
	account string
}

func (p keychainProvider) Password(context.Context) ([]byte, error) {
	value, err := keyring.Get(p.service, p.account)
	if err != nil || value == "" {
		return nil, ErrUnavailable
	}
	return []byte(value), nil
}
