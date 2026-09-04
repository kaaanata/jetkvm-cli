package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/kaaanata/jetkvm-cli/internal/cli"
	"github.com/kaaanata/jetkvm-cli/internal/confirmation"
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
	if _, err := fmt.Fprintf(i.output, "Confirm JetKVM action\n%s\nDevice: %s\nType 'yes' to continue: ", request.Summary, request.DeviceID); err != nil {
		return nil, err
	}
	answer := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(i.input).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			readErr <- err
			return
		}
		answer <- strings.TrimSpace(line)
	}()
	var approved bool
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case err := <-readErr:
		return nil, err
	case value := <-answer:
		approved = strings.EqualFold(value, "yes")
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
