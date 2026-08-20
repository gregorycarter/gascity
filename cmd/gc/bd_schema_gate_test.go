package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestBdMigrateStatusRefusal(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "status subcommand", args: []string{"migrate", "status"}, want: true},
		{name: "status with output flag", args: []string{"migrate", "status", "--json"}, want: true},
		{name: "explicit inspect", args: []string{"migrate", "--inspect", "status"}, want: false},
		{name: "explicit dry run", args: []string{"migrate", "--dry-run", "status"}, want: false},
		{name: "write-intent migrate", args: []string{"migrate"}, want: false},
		{name: "schema migration", args: []string{"migrate", "schema"}, want: false},
		{name: "unrelated command", args: []string{"list", "--status", "open"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := refuseBdMigrateStatus(tt.args)
			if (err != nil) != tt.want {
				t.Fatalf("refuseBdMigrateStatus(%v) error = %v, want refusal=%v", tt.args, err, tt.want)
			}
		})
	}
}

func TestDoBdRefusesMigrateStatusBeforeResolvingStore(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"migrate", "status"}, &stdout, &stderr); got == 0 {
		t.Fatalf("doBd(migrate status) = 0, want refusal; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "use `gc bd migrate --inspect`") {
		t.Fatalf("doBd refusal = %q, want explicit read-only guidance", stderr.String())
	}
}
