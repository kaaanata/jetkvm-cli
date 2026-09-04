package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	setupcore "github.com/kaaanata/jetkvm-cli/internal/setup"
)

const setupCommandOutputLimit = 4 << 20

type lazySetupService struct {
	load func() (*setupcore.Service, error)
}

func newLazySetupService() *lazySetupService {
	return &lazySetupService{load: sync.OnceValues(func() (*setupcore.Service, error) {
		configDirectory, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		return setupcore.NewService(setupcore.Config{
			Runner:   processRunner{},
			StateDir: filepath.Join(configDirectory, "jetkvm", "setup"),
		})
	})}
}

func (s *lazySetupService) Plan(ctx context.Context, request setupcore.PlanRequest) (setupcore.Plan, error) {
	service, err := s.load()
	if err != nil {
		return setupcore.Plan{}, err
	}
	return service.Plan(ctx, request)
}

func (s *lazySetupService) Apply(ctx context.Context, plan setupcore.Plan) (setupcore.Receipt, error) {
	service, err := s.load()
	if err != nil {
		return setupcore.Receipt{}, err
	}
	return service.Apply(ctx, plan)
}

func (s *lazySetupService) Uninstall(ctx context.Context, target setupcore.Target, dryRun bool) (setupcore.Receipt, error) {
	service, err := s.load()
	if err != nil {
		return setupcore.Receipt{}, err
	}
	return service.Uninstall(ctx, target, dryRun)
}

func (s *lazySetupService) Doctor(ctx context.Context, target setupcore.Target, version string) (setupcore.DoctorReport, error) {
	service, err := s.load()
	if err != nil {
		return setupcore.DoctorReport{}, err
	}
	return service.Doctor(ctx, target, version)
}

type processRunner struct{}

func (processRunner) Run(ctx context.Context, command setupcore.Command) (setupcore.CommandResult, error) {
	executable, err := exec.LookPath(command.Name)
	if err != nil {
		return setupcore.CommandResult{}, err
	}
	process := exec.CommandContext(ctx, executable, command.Args...)
	process.Dir = command.Dir
	stdout := newCappedBuffer(setupCommandOutputLimit)
	stderr := newCappedBuffer(setupCommandOutputLimit)
	process.Stdout = stdout
	process.Stderr = stderr
	err = process.Run()
	result := setupcore.CommandResult{Stdout: bytes.Clone(stdout.Bytes()), Stderr: bytes.Clone(stderr.Bytes())}
	if err == nil {
		return result, nil
	}
	if errors.Is(err, errCommandOutputLimit) {
		return setupcore.CommandResult{}, err
	}
	if exitError, ok := errors.AsType[*exec.ExitError](err); ok {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return setupcore.CommandResult{}, err
}

var errCommandOutputLimit = errors.New("agent host command output exceeded 4 MiB")

type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func newCappedBuffer(limit int) *cappedBuffer { return &cappedBuffer{limit: limit} }

func (b *cappedBuffer) Write(payload []byte) (int, error) {
	if len(payload) > b.limit-b.buffer.Len() {
		return 0, errCommandOutputLimit
	}
	return b.buffer.Write(payload)
}

func (b *cappedBuffer) Bytes() []byte { return b.buffer.Bytes() }

var _ setupcore.Runner = processRunner{}
