package order

import (
	"errors"
	"testing"
)

func TestTransitions(t *testing.T) {
	tests := []struct {
		from, to Status
		want     stockEffect
		ok       bool
	}{
		{StatusPendingPayment, StatusPaid, stockCommit, true},
		{StatusPendingPayment, StatusCancelled, stockRelease, true},
		{StatusPendingPayment, StatusExpired, stockRelease, true},
		{StatusPaid, StatusShipped, stockNone, true},
		{StatusShipped, StatusDelivered, stockNone, true},

		{StatusPendingPayment, StatusShipped, 0, false}, // must be paid first
		{StatusPaid, StatusCancelled, 0, false},         // refunds are out of scope
		{StatusPaid, StatusPaid, 0, false},              // no self-transitions
		{StatusDelivered, StatusShipped, 0, false},
		{StatusCancelled, StatusPendingPayment, 0, false},
		{StatusExpired, StatusPaid, 0, false},
	}
	for _, tt := range tests {
		got, err := effect(tt.from, tt.to)
		var te *TransitionError
		switch {
		case tt.ok && (err != nil || got != tt.want):
			t.Errorf("%s -> %s = %v, %v; want %v", tt.from, tt.to, got, err, tt.want)
		case !tt.ok && !errors.As(err, &te):
			t.Errorf("%s -> %s allowed, want TransitionError", tt.from, tt.to)
		}
	}
}

func TestEveryStatusIsInTheStateMachine(t *testing.T) {
	for _, s := range []Status{StatusPendingPayment, StatusPaid, StatusShipped, StatusDelivered, StatusCancelled, StatusExpired} {
		if !s.valid() {
			t.Errorf("%s missing from transitions", s)
		}
		for to := range transitions[s] {
			if !to.valid() {
				t.Errorf("%s -> %s targets an unknown status", s, to)
			}
		}
	}
	if adminTargets[StatusPaid] || adminTargets[StatusExpired] {
		t.Error("admins must not be able to mark orders paid or expired")
	}
}
