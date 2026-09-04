package main

import (
	"context"
	"errors"
	"io"

	"github.com/kaaanata/jetkvm-cli/internal/cli"
	"github.com/kaaanata/jetkvm-cli/internal/confirmation"
	"github.com/kaaanata/jetkvm-cli/internal/terminal"
)

type cliConfirmationIssuer struct {
	authority *confirmation.Authority
	input     io.Reader
	output    io.Writer
}

func (i cliConfirmationIssuer) Issue(ctx context.Context, request cli.ConfirmationRequest) (context.Context, error) {
	if !request.Interactive {
		return nil, cli.ErrConfirmationRequired
	}
	approved, err := terminal.New(i.output, request.Interactive).Confirm(ctx, i.input,
		"Confirm JetKVM action", request.Summary+"\nDevice: "+string(request.DeviceID))
	if err != nil {
		return nil, err
	}
	if !approved {
		return nil, errors.New("JetKVM action was not confirmed")
	}
	principalCtx := confirmation.WithPrincipal(ctx, confirmation.Principal{ID: "local-process", Transport: "cli"})
	proof, err := i.authority.Mint(principalCtx, request.Binding)
	if err != nil {
		return nil, err
	}
	return confirmation.WithProof(principalCtx, proof), nil
}
