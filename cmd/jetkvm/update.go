package main

import (
	"cmp"
	"context"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/kaaanata/jetkvm-cli/internal/buildinfo"
	updatecore "github.com/kaaanata/jetkvm-cli/internal/update"
)

type lazyUpdateService struct {
	load func() (*updatecore.Service, error)
}

func newLazyUpdateService(build buildinfo.Info) *lazyUpdateService {
	return &lazyUpdateService{load: sync.OnceValues(func() (*updatecore.Service, error) {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		receipts := updatecore.FileReceiptStore{}
		backend, err := updatecore.NewGitHubBackend(updatecore.GitHubBackendConfig{
			Token:             cmp.Or(os.Getenv("JETKVM_GITHUB_TOKEN"), os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN")),
			SignatureVerifier: updatecore.NewSigstoreVerifier(),
			OS:                runtime.GOOS,
			Arch:              runtime.GOARCH,
		})
		if err != nil {
			return nil, err
		}
		resolver := updatecore.PortableInstallationResolver{
			Executable: executable, Receipts: receipts, MissingOwner: inferredInstallOwner(executable),
			Version: build.Version, Channel: updatecore.ChannelStable,
		}
		return updatecore.NewService(resolver, backend, receipts, updatecore.NewFileLocker(executable))
	})}
}

func inferredInstallOwner(executable string) updatecore.Owner {
	normalized := strings.ToLower(strings.ReplaceAll(executable, "\\", "/"))
	switch {
	case strings.Contains(normalized, "/cellar/"), strings.Contains(normalized, "/homebrew/"):
		return updatecore.OwnerHomebrew
	case strings.Contains(normalized, "/scoop/"):
		return updatecore.OwnerScoop
	case strings.Contains(normalized, "/winget/") || strings.Contains(normalized, "/microsoft/winget/"):
		return updatecore.OwnerWinget
	case strings.Contains(normalized, "/go/bin/"), strings.HasSuffix(normalized, "/bin/jetkvm-dev"):
		return updatecore.OwnerSource
	default:
		return updatecore.OwnerUnknown
	}
}

func (s *lazyUpdateService) Resolve(ctx context.Context, request updatecore.Request) (updatecore.Resolution, error) {
	service, err := s.load()
	if err != nil {
		return updatecore.Resolution{}, err
	}
	return service.Resolve(ctx, request)
}

func (s *lazyUpdateService) Check(ctx context.Context, resolution updatecore.Resolution) (updatecore.CheckResult, error) {
	service, err := s.load()
	if err != nil {
		return updatecore.CheckResult{}, err
	}
	return service.Check(ctx, resolution)
}

func (s *lazyUpdateService) Plan(check updatecore.CheckResult) (updatecore.Plan, error) {
	service, err := s.load()
	if err != nil {
		return updatecore.Plan{}, err
	}
	return service.Plan(check)
}

func (s *lazyUpdateService) Apply(ctx context.Context, plan updatecore.Plan) (updatecore.Result, error) {
	service, err := s.load()
	if err != nil {
		return updatecore.Result{}, err
	}
	return service.Apply(ctx, plan)
}

func (s *lazyUpdateService) Rollback(ctx context.Context) (updatecore.Result, error) {
	service, err := s.load()
	if err != nil {
		return updatecore.Result{}, err
	}
	return service.Rollback(ctx)
}
