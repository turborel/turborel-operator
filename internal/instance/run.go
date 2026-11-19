package instance

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/turborel/turborel-operator/internal/pki"
)

// Prepare initializes only a newly authorized volume. A durable marker prevents a
// container restart from interpreting a lost data directory as a new replica group.
func Prepare(ctx context.Context, cfg Config) ([]string, string, error) {
	user, password, err := credentials()
	if err != nil {
		return nil, "", err
	}
	args := cfg.serverArgs()
	if cfg.Restore != nil {
		if err := restore(ctx, cfg); err != nil {
			return nil, "", err
		}
		return args, "", nil
	}
	_, dataErr := os.Stat(DataDir)
	if dataErr != nil && !errors.Is(dataErr, os.ErrNotExist) {
		return nil, "", dataErr
	}
	if cfg.Members == "" {
		if dataErr == nil {
			return args, "", recordInitialized(DataRoot)
		}
		if err := requireUninitialized(DataRoot); err != nil {
			return nil, "", err
		}
		if !cfg.Bootstrap {
			return nil, "", fmt.Errorf("standalone data is missing; restore from backup instead of creating an empty database")
		}
		staging := DataRoot + "/initializing"
		initArgs := cfg.serverArgs()
		initArgs[1] = staging
		// A failed owner creation leaves only staging, never a database at DataDir.
		if err := os.RemoveAll(staging); err != nil {
			return nil, "", err
		}
		initArgs = append(initArgs, "user", "add", user, "--owner")
		cmd := exec.CommandContext(ctx, "/usr/local/bin/turboreld", initArgs...)
		cmd.Stdin = strings.NewReader(password + "\n")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, "", fmt.Errorf("initialize owner: %w", err)
		}
		if err := os.Rename(staging, DataDir); err != nil {
			return nil, "", err
		}
		return args, "", recordInitialized(DataRoot)
	}
	if !cfg.Bootstrap {
		return args, "", nil
	}
	marker := DataRoot + "/.operator-bootstrap"
	file, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		// Existing durable replication logs can safely repeat owner creation if the
		// first attempt stopped before committing it. An empty log must only join.
		entries, readErr := os.ReadDir(filepath.Join(DataDir, "log"))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, "", readErr
		}
		if len(entries) == 0 {
			return args, "", nil
		}
	} else {
		if err != nil {
			return nil, "", err
		}
		if err := file.Sync(); err != nil {
			return nil, "", errors.Join(err, file.Close())
		}
		if err := file.Close(); err != nil {
			return nil, "", err
		}
		if err := syncDirectory(DataRoot); err != nil {
			return nil, "", err
		}
	}
	return append(args, "--owner", user), password + "\n", nil
}

func restore(ctx context.Context, cfg Config) error {
	if _, err := os.Stat(DataDir); err == nil {
		return recordInitialized(DataRoot)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := requireUninitialized(DataRoot); err != nil {
		return err
	}
	r := cfg.Restore
	args := append([]string{"--dir", DataDir}, cfg.keyArgs()...)
	args = append(args, "--restore", filepath.Join(RestoreDir, r.BackupPath))
	if r.BackupID != "" {
		args = append(args, "--backup", r.BackupID)
	}
	if r.ArchivePath != "" {
		args = append(args, "--archive", filepath.Join(RestoreDir, r.ArchivePath))
	}
	switch {
	case r.UntilTime != "":
		args = append(args, "--until-time", r.UntilTime)
	case r.UntilLSN != nil:
		args = append(args, "--until-lsn", fmt.Sprint(*r.UntilLSN))
	case r.UntilPoint != "":
		args = append(args, "--until-point", r.UntilPoint)
	}
	cmd := exec.CommandContext(ctx, "/usr/local/bin/turboreld", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restore database: %w", err)
	}
	return recordInitialized(DataRoot)
}

// Run supervises the database and forwards shutdown to its process.
func Run(ctx context.Context, cfg Config) (retErr error) {
	args, stdin, err := Prepare(ctx, cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(Socket), 0700); err != nil {
		return err
	}
	tlsData := map[string][]byte{}
	for _, name := range []string{"ca.crt", "tls.crt", "tls.key"} {
		value, err := os.ReadFile(filepath.Join(TLSDir, name))
		if err != nil {
			return err
		}
		tlsData[name] = value
	}
	tlsConfig, err := pki.TLSConfig(tlsData, "")
	if err != nil {
		return err
	}
	tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	var mu sync.RWMutex
	status := Status{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		current := status
		mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		if time.Since(current.CheckedAt) > 15*time.Second {
			current.Ready = false
			current.Role = ""
		}
		if err := json.NewEncoder(w).Encode(current); err != nil {
			return
		}
	})
	server := &http.Server{Addr: ":8000", Handler: mux, TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServeTLS("", "") }()
	defer func() { retErr = errors.Join(retErr, server.Close()) }()
	child := exec.Command("/usr/local/bin/turboreld", args...)
	child.Stdin = strings.NewReader(stdin)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	pollDone := make(chan struct{})
	pollErr := make(chan error, 1)
	go func() {
		defer close(pollDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			current := LocalStatus(pollCtx, cfg.Members != "")
			mu.Lock()
			status = current
			mu.Unlock()
			if err := writeStatus(current); err != nil {
				pollErr <- err
				return
			}
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	var cause error
	select {
	case err := <-exited:
		cancel()
		<-pollDone
		return err
	case <-ctx.Done():
	case err := <-pollErr:
		cause = fmt.Errorf("write readiness: %w", err)
	case err := <-serverErr:
		cause = fmt.Errorf("status server: %w", err)
	}
	cancel()
	<-pollDone
	if err := writeStatus(Status{}); err != nil {
		cause = errors.Join(cause, err)
	}
	// SIGTERM gives turboreld its own thirty-second drain budget; the Pod has sixty seconds.
	if err := child.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		cause = errors.Join(cause, err)
	}
	timer := time.NewTimer(40 * time.Second)
	defer timer.Stop()
	select {
	case err := <-exited:
		// Keep the Pod network alive long enough for peers to observe the closed
		// replication sockets. Removing its network immediately can strand a
		// peer's write-only TCP connection until the kernel retransmit timeout.
		time.Sleep(5 * time.Second)
		return errors.Join(cause, err)
	case <-timer.C:
		if err := child.Process.Kill(); err != nil {
			cause = errors.Join(cause, err)
		}
		<-exited
		return errors.Join(cause, fmt.Errorf("database exceeded shutdown deadline"))
	}
}

func writeStatus(status Status) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	temp := StatusPath + ".tmp"
	if err := os.WriteFile(temp, data, 0600); err != nil {
		return err
	}
	return os.Rename(temp, StatusPath)
}

func Probe() error {
	data, err := os.ReadFile(StatusPath)
	if err != nil {
		return err
	}
	var status Status
	if err := json.Unmarshal(data, &status); err != nil {
		return err
	}
	if !status.Ready || time.Since(status.CheckedAt) > 15*time.Second {
		return fmt.Errorf("database is not ready")
	}
	return nil
}

// Install copies this statically linked executable into the shared init volume.
func Install(destination string) (retErr error) {
	source, err := os.Executable()
	if err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, in.Close()) }()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Close())
}

// A synced marker outlives accidental removal of the database directory. It also
// stops a restore specification from silently rolling a running database back.
func recordInitialized(root string) error {
	file, err := os.OpenFile(filepath.Join(root, ".operator-initialized"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	if err := errors.Join(syncErr, file.Close()); err != nil {
		return err
	}
	return syncDirectory(root)
}

func requireUninitialized(root string) error {
	_, err := os.Stat(filepath.Join(root, ".operator-initialized"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("database directory was lost after initialization; refusing to replace committed data with an empty or restored database")
}

func syncDirectory(root string) error {
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	return errors.Join(syncErr, directory.Close())
}
