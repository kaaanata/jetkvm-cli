package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/video"
)

func TestVideoFailureTaxonomy(t *testing.T) {
	for _, test := range []struct {
		err  error
		kind string
		code int
	}{
		{video.ErrVideoUnavailable, "unavailable", ExitUnavailable},
		{video.ErrPipelineClosed, "unavailable", ExitUnavailable},
		{video.ErrDecodeFailed, "unavailable", ExitUnavailable},
		{video.ErrDimensionsExceeded, "unavailable", ExitUnavailable},
		{video.ErrFrameStale, "observation_stale", ExitConflict},
		{video.ErrGenerationMismatch, "control_generation_mismatch", ExitConflict},
		{video.ErrDecoderUnavailable, "capability_unavailable", ExitUnsupported},
		{domain.ErrCapabilityUnavailable, "capability_unavailable", ExitUnsupported},
	} {
		t.Run(test.err.Error(), func(t *testing.T) {
			// Capture timeouts can join a source error; keep the more specific kind
			// and never imply that preceding input can be replayed.
			detail := classifyFailure(errors.Join(context.DeadlineExceeded, fmt.Errorf("capture: %w", test.err)))
			if detail.Kind != test.kind || detail.ExitCode != test.code || detail.Retryable {
				t.Fatalf("classification=%+v", detail)
			}
		})
	}
}
