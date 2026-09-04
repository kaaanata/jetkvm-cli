package credentials

import (
	"errors"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/config"
)

func TestEnvironmentProvider(t *testing.T) {
	t.Setenv("JETKVM_TEST_PASSWORD", "secret")
	provider, err := New(config.CredentialConfig{
		Provider: config.CredentialEnvironment,
		Variable: "JETKVM_TEST_PASSWORD",
	})
	if err != nil {
		t.Fatal(err)
	}
	password, err := provider.Password(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if string(password) != "secret" {
		t.Fatal("environment provider returned an unexpected value")
	}
	clear(password)
}

func TestEnvironmentProviderMissing(t *testing.T) {
	provider, err := New(config.CredentialConfig{
		Provider: config.CredentialEnvironment,
		Variable: "JETKVM_TEST_MISSING_PASSWORD",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Password(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Password() error = %v, want unavailable", err)
	}
}

func TestNoPasswordProvider(t *testing.T) {
	provider, err := New(config.CredentialConfig{Provider: config.CredentialNoPassword})
	if err != nil {
		t.Fatal(err)
	}
	if provider != nil {
		t.Fatal("no-password configuration returned a credential provider")
	}
}
