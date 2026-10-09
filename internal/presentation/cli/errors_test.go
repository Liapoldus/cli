package cli

import (
	"testing"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
)

func TestHTTPStatusExitMapping(t *testing.T) {
	tests := []struct {
		status int
		want   int
	}{
		{status: 401, want: application.ExitTarget},
		{status: 403, want: application.ExitTarget},
		{status: 409, want: application.ExitConflict},
		{status: 422, want: application.ExitValidation},
		{status: 500, want: application.ExitOperation},
	}
	for _, test := range tests {
		if got := deploymentCode(domain.CoreHTTPError{Status: test.status}); got != test.want {
			t.Errorf("status=%d got=%d want=%d", test.status, got, test.want)
		}
	}
}

func TestParseOptionsAcceptsTokenStdin(t *testing.T) {
	options, err := parseOptions([]string{"--token-stdin"})
	if err != nil || !options.tokenStdin {
		t.Fatalf("options=%+v err=%v", options, err)
	}
}
