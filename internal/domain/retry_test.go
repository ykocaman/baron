package domain

import "testing"

func TestNextRetry(t *testing.T) {
	tests := []struct {
		retriesUsed int
		budget      int
		want        RetryDecision
	}{
		{0, 3, RetryAgain},
		{1, 3, RetryAgain},
		{2, 3, RetryAgain},
		{3, 3, RetryExhausted},
		{4, 3, RetryExhausted},
		{0, 0, RetryExhausted},
	}
	for _, tt := range tests {
		if got := NextRetry(tt.retriesUsed, tt.budget); got != tt.want {
			t.Errorf("NextRetry(%d, %d) = %v, want %v", tt.retriesUsed, tt.budget, got, tt.want)
		}
	}
}
