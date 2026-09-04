package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/cli"
	"github.com/kaaanata/jetkvm-cli/internal/confirmation"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

func TestCharmConfirmationKeepsAuthorityBinding(t *testing.T) {
	authority, err := confirmation.NewAuthority(confirmation.Config{Key: bytes.Repeat([]byte{1}, 32), Nonces: confirmation.NewMemoryNonceStore()})
	if err != nil {
		t.Fatal(err)
	}
	binding := confirmation.Binding{DeviceID: "fixture-device", Generation: 7, Effect: domain.EffectPower, Action: "power.reset", ArgumentsDigest: confirmation.DigestArguments([]byte("canonical")), PolicyRevision: "sha256:policy"}
	request := cli.ConfirmationRequest{Interactive: true, DeviceID: binding.DeviceID, Summary: "Reset fixture power", Binding: binding}
	output := new(bytes.Buffer)
	issuer := cliConfirmationIssuer{authority: authority, input: strings.NewReader("yes\n"), output: output}
	ctx, err := issuer.Issue(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	wrong := binding
	wrong.Generation++
	if err := authority.VerifyAndConsume(ctx, wrong); !errors.Is(err, confirmation.ErrProofMismatch) {
		t.Fatalf("binding changed: %v", err)
	}
	if err := authority.VerifyAndConsume(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if err := authority.VerifyAndConsume(ctx, binding); !errors.Is(err, confirmation.ErrProofReplayed) {
		t.Fatalf("proof reused: %v", err)
	}
	if !strings.Contains(output.String(), string(binding.DeviceID)) {
		t.Fatal("device missing from confirmation")
	}
}

func TestCharmConfirmationNeverMintsOnDecline(t *testing.T) {
	for _, answer := range []string{"no\n", "\n", ""} {
		issuer := cliConfirmationIssuer{input: strings.NewReader(answer), output: new(bytes.Buffer)}
		if ctx, err := issuer.Issue(t.Context(), cli.ConfirmationRequest{Interactive: true}); ctx != nil || err == nil {
			t.Fatal("decline minted proof")
		}
	}
	issuer := cliConfirmationIssuer{input: strings.NewReader("yes\n"), output: new(bytes.Buffer)}
	if ctx, err := issuer.Issue(t.Context(), cli.ConfirmationRequest{}); ctx != nil || !errors.Is(err, cli.ErrConfirmationRequired) {
		t.Fatal("noninteractive confirmation was accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if confirmed, err := issuer.Issue(ctx, cli.ConfirmationRequest{Interactive: true}); confirmed != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled confirmation was accepted")
	}
}
