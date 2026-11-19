package instance

import (
	"slices"
	"strings"
	"testing"
)

func TestNativeServerConfiguration(t *testing.T) {
	t.Parallel()
	cfg := Config{Member: 2, Members: "1=db1:7423,2=db2:7423,3=db3:7423", Settings: map[string]string{"STORAGE.ENCODINGS": "LZ4"}}
	args := cfg.serverArgs()
	for _, expected := range []string{"REPLICATION.REPLICA=2", "REPLICATION.LISTEN=0.0.0.0:7423", "REPLICATION.TLS=ON",
		"SERVER.TLS=REQUIRED", "SERVER.TRUST_LOCAL=OFF", "SERVER.SCRAM_WITHOUT_TLS=OFF", "STORAGE.ENCODINGS=LZ4"} {
		if !slices.Contains(args, expected) {
			t.Fatalf("missing %s", expected)
		}
	}
	if slices.Contains(args, "--owner") {
		t.Fatal("ordinary restart is authorized to create a new replica group")
	}
	if !slices.Equal(args, cfg.serverArgs()) {
		t.Fatal("configuration rendering is nondeterministic")
	}
}
func TestQuoteLiteral(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, value, want string }{
		{name: "plain", value: "backup", want: "'backup'"},
		{name: "quote", value: "a'b", want: "'a''b'"},
		{name: "SQL syntax", value: "'; DROP USER admin; --", want: "'''; DROP USER admin; --'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := QuoteLiteral(test.value); got != test.want {
				t.Fatalf("got %s want %s", got, test.want)
			}
			if !strings.HasPrefix(test.want, "'") {
				t.Fatal("invalid fixture")
			}
		})
	}
}

func TestLostInitializedDataCannotBootstrapOrRestoreAgain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := requireUninitialized(root); err != nil {
		t.Fatal(err)
	}
	if err := recordInitialized(root); err != nil {
		t.Fatal(err)
	}
	if err := requireUninitialized(root); err == nil {
		t.Fatal("initialized volume was allowed to bootstrap again")
	}
	if err := recordInitialized(root); err != nil {
		t.Fatal("recording initialization must be idempotent", err)
	}
}
