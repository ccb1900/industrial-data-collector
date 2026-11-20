package websafety

import "testing"

func TestValidateBind(t *testing.T) {
	for _, tc := range []struct {
		addr        string
		allowRemote bool
		wantErr     error
	}{
		{"127.0.0.1:8080", false, nil},
		{"localhost:8080", false, nil},
		{"[::1]:8080", false, nil},
		{":8080", false, ErrNonLoopback},
		{"0.0.0.0:8080", false, ErrNonLoopback},
		{"192.168.1.10:8080", false, ErrNonLoopback},
		{":8080", true, nil},
	} {
		if got := ValidateBind(tc.addr, tc.allowRemote); got != tc.wantErr {
			t.Errorf("ValidateBind(%q, %v) = %v, want %v", tc.addr, tc.allowRemote, got, tc.wantErr)
		}
	}
}
