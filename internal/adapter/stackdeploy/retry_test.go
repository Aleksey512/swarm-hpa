package stackdeploy

import (
	"context"
	"errors"
	"testing"
	"time"
)

// outOfSequence is the real error string Swarm emits when a concurrent ServiceUpdate
// (autoscaler/healer) bumps a service's Version.Index during a `docker stack deploy`.
var outOfSequence = errors.New("rpc error: code = Unknown desc = update out of sequence")

func TestWithRetry(t *testing.T) {
	cases := []struct {
		name       string
		updateErrs []error // one per DeployFunc call, in order; nil = success
		wantErr    bool
		wantCalls  int
	}{
		{
			name:       "succeeds first try",
			updateErrs: []error{nil},
			wantErr:    false,
			wantCalls:  1,
		},
		{
			name:       "retries out-of-sequence then succeeds",
			updateErrs: []error{outOfSequence, outOfSequence, nil},
			wantErr:    false,
			wantCalls:  3,
		},
		{
			name:       "non-conflict fails fast",
			updateErrs: []error{errors.New("network unreachable")},
			wantErr:    true,
			wantCalls:  1,
		},
		{
			name:       "always conflicting exhausts attempts",
			updateErrs: []error{outOfSequence, outOfSequence, outOfSequence},
			wantErr:    true,
			wantCalls:  deployMaxAttempts,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			fake := func(_ context.Context, _, _, _ string) error {
				calls++
				if calls <= len(c.updateErrs) {
					return c.updateErrs[calls-1]
				}
				return nil
			}
			err := WithRetry(fake, discardLog())(context.Background(), "stk", "compose.yaml", "changed")
			if c.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if calls != c.wantCalls {
				t.Errorf("deploy calls = %d, want %d", calls, c.wantCalls)
			}
		})
	}
}

// A context that expires during the inter-attempt backoff must abort the retry loop
// rather than burning all attempts.
func TestWithRetryHonorsContextCancellation(t *testing.T) {
	fake := func(_ context.Context, _, _, _ string) error {
		return outOfSequence // always conflict → forces a backoff wait
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := WithRetry(fake, discardLog())(ctx, "stk", "compose.yaml", "changed")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded during backoff, got %v", err)
	}
}
