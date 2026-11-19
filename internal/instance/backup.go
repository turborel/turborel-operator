package instance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type BackupConfig struct {
	Host           string `json:"host"`
	RepositoryPath string `json:"repositoryPath"`
	ParentID       string `json:"parentID,omitempty"`
	Label          string `json:"label"`
	Verify         bool   `json:"verify"`
	Keys           bool   `json:"keys"`
	ChecksumKey    string `json:"checksumKey,omitempty"`
}

type BackupResult struct {
	BackupID string `json:"backupID"`
	Verified bool   `json:"verified"`
}

func Backup(ctx context.Context, cfg BackupConfig) error {
	address := []string{"--host", cfg.Host, "--tls", TLSDir + "/ca.crt"}
	target := filepath.Join(BackupDir, cfg.RepositoryPath)
	query := "BACKUP DATABASE TO " + QuoteLiteral(target)
	if cfg.ParentID != "" {
		query += " INCREMENTAL FROM " + QuoteLiteral(cfg.ParentID)
	}
	query += " LABEL " + QuoteLiteral(cfg.Label)
	if _, err := SQL(ctx, address, query); err != nil {
		return fmt.Errorf("take backup: %w", err)
	}
	query = "SELECT BACKUP_ID FROM INFORMATION_SCHEMA.BACKUPS WHERE LABEL = " + QuoteLiteral(cfg.Label) + " AND STATE = 'COMPLETED'"
	id, err := SQL(ctx, address, query)
	if err != nil {
		return fmt.Errorf("find completed backup: %w", err)
	}
	if id == "" || strings.ContainsAny(id, "\n\r|") {
		return fmt.Errorf("backup did not return exactly one ID")
	}
	if cfg.Verify {
		// Global key options must precede the backup command in turboreld's CLI.
		args := (Config{Keys: cfg.Keys, ChecksumKey: cfg.ChecksumKey}).keyArgs()
		args = append(args, "--verify-backup", target, "--backup", id)
		cmd := exec.CommandContext(ctx, "/usr/local/bin/turboreld", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("verify backup: %w", err)
		}
	}
	result, err := json.Marshal(BackupResult{BackupID: id, Verified: cfg.Verify})
	if err != nil {
		return err
	}
	return os.WriteFile("/dev/termination-log", result, 0600)
}
