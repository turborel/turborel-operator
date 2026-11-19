// Package instance supervises turboreld using the CLI shipped in the database image.
package instance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	databasev1alpha1 "github.com/turborel/turborel-operator/api/v1alpha1"
)

const (
	DataRoot   = "/var/lib/turborel"
	DataDir    = DataRoot + "/data"
	Socket     = "/run/turborel/database.sock"
	ConfigPath = "/etc/turborel/config/config.json"
	TLSDir     = "/etc/turborel/tls"
	OwnerDir   = "/etc/turborel/owner"
	KeysDir    = "/etc/turborel/keys"
	BackupDir  = "/backups"
	RestoreDir = "/restore"
	StatusPath = "/run/turborel/status.json"
)

type Config struct {
	Member      int32                         `json:"member"`
	Members     string                        `json:"members,omitempty"`
	Bootstrap   bool                          `json:"bootstrap"`
	Settings    map[string]string             `json:"settings"`
	Keys        bool                          `json:"keys,omitempty"`
	ChecksumKey string                        `json:"checksumKey,omitempty"`
	Restore     *databasev1alpha1.RestoreSpec `json:"restore,omitempty"`
}

type Status struct {
	// Healthy gates controlled updates; Ready only describes the SQL probe.
	Healthy   bool      `json:"healthy"`
	Members   int32     `json:"members"`
	Epoch     uint64    `json:"epoch"`
	View      uint64    `json:"view"`
	Ready     bool      `json:"ready"`
	Role      string    `json:"role"`
	CheckedAt time.Time `json:"checkedAt"`
}

func credentials() (string, string, error) {
	user, err := os.ReadFile(filepath.Join(OwnerDir, "username"))
	if err != nil {
		return "", "", err
	}
	password, err := os.ReadFile(filepath.Join(OwnerDir, "password"))
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(string(user)), string(password), nil
}

func (c Config) keyArgs() []string {
	args := []string{}
	if c.Keys {
		args = append(args, "--keys", KeysDir)
	}
	if c.ChecksumKey != "" {
		args = append(args, "--checksum-key", c.ChecksumKey)
	}
	return args
}

func (c Config) serverArgs() []string {
	args := append([]string{"--dir", DataDir}, c.keyArgs()...)
	settings := map[string]string{}
	for k, v := range c.Settings {
		settings[k] = v
	}
	settings["SERVER.HOST"] = "0.0.0.0"
	settings["SERVER.PORT"] = "7422"
	settings["SERVER.SOCKET"] = Socket
	settings["SERVER.TLS"] = "REQUIRED"
	settings["SERVER.TLS_CERTIFICATE"] = TLSDir + "/tls.crt"
	settings["SERVER.TLS_KEY"] = TLSDir + "/tls.key"
	settings["SERVER.TRUST_LOCAL"] = "OFF"
	settings["SERVER.SCRAM_WITHOUT_TLS"] = "OFF"
	settings["SERVER.SHUTDOWN_TIMEOUT_MS"] = "30000"
	if c.Members != "" {
		settings["REPLICATION.REPLICA"] = fmt.Sprint(c.Member)
		settings["REPLICATION.MEMBERS"] = c.Members
		settings["REPLICATION.LISTEN"] = "0.0.0.0:7423"
		settings["REPLICATION.CA"] = TLSDir + "/ca.crt"
		settings["REPLICATION.TLS"] = "ON"
	}
	names := make([]string, 0, len(settings))
	for name := range settings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		args = append(args, "--set", name+"="+settings[name])
	}
	return args
}

// SQL runs with a bounded lifetime and never places the password in process arguments.
func SQL(ctx context.Context, address []string, statement string) (string, error) {
	user, password, err := credentials()
	if err != nil {
		return "", err
	}
	args := append([]string{}, address...)
	args = append(args, "--user", user, "--stop-on-error", "--quiet", "--unaligned", "--tuples-only",
		"--connect-timeout", "5", "--read-timeout", "0", "--command", statement)
	cmd := exec.CommandContext(ctx, "/usr/local/bin/turborel", args...)
	cmd.Env = append(os.Environ(), "TURBOREL_PASSWORD="+password)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("SQL command failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func LocalStatus(ctx context.Context, replicated bool) Status {
	timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	query := "VALUES (1)"
	if replicated {
		query = "SELECT IS_SELF, ROLE, EPOCH, VIEW_NUMBER, IS_CONNECTED, LAST_HEARD_MS FROM INFORMATION_SCHEMA.REPLICAS ORDER BY MEMBER"
	}
	out, err := SQL(timeout, []string{"--socket", Socket}, query)
	status := Status{CheckedAt: time.Now().UTC()}
	if err != nil {
		return status
	}
	if !replicated && out == "1" {
		status.Ready = true
		status.Role = "primary"
		status.Healthy = true
		status.Members = 1
		return status
	}
	parsed := replicaStatus(out)
	parsed.CheckedAt = status.CheckedAt
	return parsed
}

// QuoteLiteral keeps SQL values separate from syntax; the native CLI has no bind-parameter flag.
func QuoteLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
