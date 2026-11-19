package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	db "github.com/turborel/turborel-operator/api/v1alpha1"
	"github.com/turborel/turborel-operator/internal/controller"
	"github.com/turborel/turborel-operator/internal/instance"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "instance" {
		if err := runInstance(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := runManager(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func runInstance(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("instance command required: install, run, probe, backup")
	}
	switch args[0] {
	case "install":
		if len(args) != 2 {
			return fmt.Errorf("install requires a destination")
		}
		return instance.Install(args[1])
	case "probe":
		return instance.Probe()
	case "run":
		data, err := os.ReadFile(instance.ConfigPath)
		if err != nil {
			return err
		}
		var cfg instance.Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		return instance.Run(ctrl.SetupSignalHandler(), cfg)
	case "backup":
		var cfg instance.BackupConfig
		if err := json.Unmarshal([]byte(os.Getenv("BACKUP_CONFIG")), &cfg); err != nil {
			return err
		}
		return instance.Backup(ctrl.SetupSignalHandler(), cfg)
	default:
		return fmt.Errorf("unknown instance command %q", args[0])
	}
}
func runManager() error {
	var metricsAddress, probeAddress, operatorImage, namespace string
	var leaderElection, secureMetrics bool
	flag.StringVar(&metricsAddress, "metrics-bind-address", "0", "Metrics address; 0 disables metrics")
	flag.StringVar(&probeAddress, "health-probe-bind-address", ":8081", "Manager health address")
	flag.StringVar(&operatorImage, "operator-image", os.Getenv("OPERATOR_IMAGE"), "Image containing this manager and instance binary")
	flag.StringVar(&namespace, "watch-namespace", os.Getenv("WATCH_NAMESPACE"), "Namespace to watch; empty watches all")
	flag.BoolVar(&leaderElection, "leader-elect", true, "Enable manager leader election")
	flag.BoolVar(&secureMetrics, "metrics-secure", true, "Protect metrics with TLS and Kubernetes authorization")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	if operatorImage == "" {
		return fmt.Errorf("operator-image or OPERATOR_IMAGE is required")
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(db.AddToScheme(scheme))
	metrics := metricsserver.Options{BindAddress: metricsAddress, SecureServing: secureMetrics}
	if secureMetrics {
		metrics.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	options := ctrl.Options{Scheme: scheme, Metrics: metrics, HealthProbeBindAddress: probeAddress,
		LeaderElection: leaderElection, LeaderElectionID: "turborel-operator.database.turborel.com"}
	if namespace != "" {
		options.Cache = cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}}
	}
	manager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), options)
	if err != nil {
		return err
	}
	if err := (&controller.ClusterReconciler{Client: manager.GetClient(), Scheme: scheme, OperatorImage: operatorImage}).SetupWithManager(manager); err != nil {
		return err
	}
	if err := (&controller.BackupReconciler{Client: manager.GetClient(), Scheme: scheme, OperatorImage: operatorImage}).SetupWithManager(manager); err != nil {
		return err
	}
	if err := (&controller.ScheduledBackupReconciler{Client: manager.GetClient(), Scheme: scheme}).SetupWithManager(manager); err != nil {
		return err
	}
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return manager.Start(ctrl.SetupSignalHandler())
}
