/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/spf13/cobra"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	coreclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/cli"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/featuregate"
	"k8s.io/component-base/logs"
	logsapi "k8s.io/component-base/logs/api/v1"
	_ "k8s.io/component-base/logs/json/register" // for JSON log output support
	"k8s.io/component-base/metrics/legacyregistry"
	_ "k8s.io/component-base/metrics/prometheus/clientgo/leaderelection" // register leader election in the default legacy registry
	_ "k8s.io/component-base/metrics/prometheus/restclient"              // for client metric registration
	_ "k8s.io/component-base/metrics/prometheus/version"                 // for version metric registration
	_ "k8s.io/component-base/metrics/prometheus/workqueue"               // register work queues in the default legacy registry
	"k8s.io/component-base/term"
	"k8s.io/dynamic-resource-allocation/controller"
	"k8s.io/dynamic-resource-allocation/leaderelection"
	"k8s.io/klog/v2"

	myclientset "github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/clientset/versioned"
	mycrd "github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/v1alpha/api"
)

type flags_t struct {
	kubeconfig   *string
	kubeAPIQPS   *float32
	kubeAPIBurst *int
	workers      *int

	httpEndpoint *string
	metricsPath  *string
	profilePath  *string

	enableLeaderElection        *bool
	leaderElectionNamespace     *string
	leaderElectionLeaseDuration *time.Duration
	leaderElectionRenewDeadline *time.Duration
	leaderElectionRetryPeriod   *time.Duration
}

type clientset_t struct {
	core    coreclientset.Interface
	example myclientset.Interface
}

type config_t struct {
	namespace string
	flags     *flags_t
	csconfig  *rest.Config
	clientset *clientset_t
	ctx       context.Context
	mux       *http.ServeMux
}

func main() {
	command := newCommand()
	code := cli.Run(command)
	os.Exit(code)
}

// NewCommand creates a *cobra.Command object with default parameters.
func newCommand() *cobra.Command {
	logsconfig := logsapi.NewLoggingConfiguration()
	fgate := featuregate.NewFeatureGate()
	utilruntime.Must(logsapi.AddFeatureGates(fgate))

	cmd := &cobra.Command{
		Use:   "controller",
		Short: "Examlpe Mydevice resource-driver controller",
		Long:  "Examlpe Mydevice resource-driver controller handles allocations and deallocations for resource-claims",
	}

	flags := addFlags(cmd, logsconfig, fgate)

	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// Activate logging as soon as possible, after that
		// show flags with the final logging configuration.
		if err := logsapi.ValidateAndApply(logsconfig, fgate); err != nil {
			return err
		}

		return nil
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		mux := http.NewServeMux()

		csconfig, err := getClientsetConfig(flags)
		if err != nil {
			return fmt.Errorf("create client configuration: %v", err)
		}

		coreclient, err := coreclientset.NewForConfig(csconfig)
		if err != nil {
			return fmt.Errorf("create core client: %v", err)
		}

		myclient, err := myclientset.NewForConfig(csconfig)
		if err != nil {
			return fmt.Errorf("create Example client: %v", err)
		}

		nsname, nsnamefound := os.LookupEnv("POD_NAMESPACE")
		if !nsnamefound {
			nsname = "default"
		}

		config := &config_t{
			ctx:       ctx,
			mux:       mux,
			flags:     flags,
			csconfig:  csconfig,
			namespace: nsname,
			clientset: &clientset_t{
				coreclient,
				myclient,
			},
		}

		if *flags.httpEndpoint != "" {
			err = setupHTTPEndpoint(config)
			if err != nil {
				return fmt.Errorf("create http endpoint: %v", err)
			}
		}

		if *flags.enableLeaderElection {
			err = startControllerWithLeader(config)
			if err != nil {
				return fmt.Errorf("start controller with leader: %v", err)
			}
		} else {
			StartController(config)
		}

		return nil
	}

	return cmd
}

func addFlags(cmd *cobra.Command, logsconfig *logsapi.LoggingConfiguration, fgate featuregate.MutableFeatureGate) *flags_t {
	flags := &flags_t{}

	sharedFlagSets := cliflag.NamedFlagSets{}
	fs := sharedFlagSets.FlagSet("logging")
	logsapi.AddFlags(logsconfig, fs)
	logs.AddFlags(fs, logs.SkipLoggingConfigurationFlags())

	fs = sharedFlagSets.FlagSet("Kubernetes client")
	flags.kubeconfig = fs.String("kubeconfig", "", "Absolute path to the kube.config file. Either this or KUBECONFIG need to be set if the driver is being run out of cluster.")
	flags.kubeAPIQPS = fs.Float32("kube-api-qps", 5, "QPS to use while communicating with the kubernetes apiserver.")
	flags.kubeAPIBurst = fs.Int("kube-api-burst", 10, "Burst to use while communicating with the kubernetes apiserver.")
	flags.workers = fs.Int("workers", 10, "Concurrency to process multiple claims")

	fs = sharedFlagSets.FlagSet("http server")
	flags.httpEndpoint = fs.String("http-endpoint", "",
		"The TCP network address where the HTTP server for diagnostics, including pprof, metrics and (if applicable) leader election health check, will listen (example: `:8080`). The default is the empty string, which means the server is disabled.")
	flags.metricsPath = fs.String("metrics-path", "/metrics", "The HTTP path where Prometheus metrics will be exposed, disabled if empty.")
	flags.profilePath = fs.String("pprof-path", "", "The HTTP path where pprof profiling will be available, disabled if empty.")

	fs = sharedFlagSets.FlagSet("leader election")
	flags.enableLeaderElection = fs.Bool("leader-election", false,
		"Enables leader election. If leader election is enabled, additional RBAC rules are required.")
	flags.leaderElectionNamespace = fs.String("leader-election-namespace", "",
		"Namespace where the leader election resource lives. Defaults to the pod namespace if not set.")
	flags.leaderElectionLeaseDuration = fs.Duration("leader-election-lease-duration", 15*time.Second,
		"Duration, in seconds, that non-leader candidates will wait to force acquire leadership.")
	flags.leaderElectionRenewDeadline = fs.Duration("leader-election-renew-deadline", 10*time.Second,
		"Duration, in seconds, that the acting leader will retry refreshing leadership before giving up.")
	flags.leaderElectionRetryPeriod = fs.Duration("leader-election-retry-period", 5*time.Second,
		"Duration, in seconds, the LeaderElector clients should wait between tries of actions.")

	fs = sharedFlagSets.FlagSet("other")
	fgate.AddFlag(fs)

	fs = cmd.PersistentFlags()
	for _, f := range sharedFlagSets.FlagSets {
		fs.AddFlagSet(f)
	}

	// SetUsageAndHelpFunc takes care of flag grouping. However,
	// it doesn't support listing child commands. We add those
	// to cmd.Use.
	cols, _, _ := term.TerminalSize(cmd.OutOrStdout())
	cliflag.SetUsageAndHelpFunc(cmd, sharedFlagSets, cols)

	return flags
}

func getClientsetConfig(f *flags_t) (*rest.Config, error) {
	var csconfig *rest.Config

	klog.V(5).Infof("Getting client config")

	kubeconfigEnv := os.Getenv("KUBECONFIG")
	if kubeconfigEnv != "" {
		klog.V(5).Infof("Found KUBECONFIG environment variable set, using that..")
		*f.kubeconfig = kubeconfigEnv
	}

	var err error
	if *f.kubeconfig == "" {
		csconfig, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("create in-cluster client configuration: %v", err)
		}
	} else {
		csconfig, err = clientcmd.BuildConfigFromFlags("", *f.kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("create out-of-cluster client configuration: %v", err)
		}
	}

	csconfig.QPS = *f.kubeAPIQPS
	csconfig.Burst = *f.kubeAPIBurst

	return csconfig, nil
}

func setupHTTPEndpoint(config *config_t) error {
	klog.V(5).Infof("Setting up HTTP endpoint")

	if *config.flags.metricsPath != "" {
		// To collect metrics data from the metric handler itself, we
		// let it register itself and then collect from that registry.
		reg := prometheus.NewRegistry()
		gatherers := prometheus.Gatherers{
			// Include Go runtime and process metrics:
			// https://github.com/kubernetes/kubernetes/blob/9780d88cb6a4b5b067256ecb4abf56892093ee87/staging/src/k8s.io/component-base/metrics/legacyregistry/registry.go#L46-L49
			legacyregistry.DefaultGatherer,
		}
		gatherers = append(gatherers, reg)

		actualPath := path.Join("/", *config.flags.metricsPath)
		klog.V(3).InfoS("Starting metrics", "path", actualPath)
		// This is similar to k8s.io/component-base/metrics HandlerWithReset
		// except that we gather from multiple sources.
		config.mux.Handle(actualPath,
			promhttp.InstrumentMetricHandler(
				reg,
				promhttp.HandlerFor(gatherers, promhttp.HandlerOpts{})))
	}

	if *config.flags.profilePath != "" {
		actualPath := path.Join("/", *config.flags.profilePath)
		klog.V(3).InfoS("Starting profiling", "path", actualPath)
		config.mux.HandleFunc(path.Join("/", *config.flags.profilePath), pprof.Index)
		config.mux.HandleFunc(path.Join("/", *config.flags.profilePath, "cmdline"), pprof.Cmdline)
		config.mux.HandleFunc(path.Join("/", *config.flags.profilePath, "profile"), pprof.Profile)
		config.mux.HandleFunc(path.Join("/", *config.flags.profilePath, "symbol"), pprof.Symbol)
		config.mux.HandleFunc(path.Join("/", *config.flags.profilePath, "trace"), pprof.Trace)
	}

	listener, err := net.Listen("tcp", *config.flags.httpEndpoint)
	if err != nil {
		return fmt.Errorf("Listen on HTTP endpoint: %v", err)
	}

	go func() {
		klog.V(3).InfoS("Starting HTTP server", "endpoint", *config.flags.httpEndpoint)
		err := http.Serve(listener, config.mux)
		if err != nil {
			klog.ErrorS(err, "HTTP server failed")
			klog.FlushAndExit(klog.ExitFlushTimeout, 1)
		}
	}()

	return nil
}

func startControllerWithLeader(config *config_t) error {
	klog.V(3).Infof("Starting controller with leader election")
	// This must not change between releases.
	lockName := mycrd.ApiGroupName

	// Create a new clientset for leader election
	// to avoid starving it when the normal traffic
	// exceeds the QPS+burst limits.
	clientset, err := coreclientset.NewForConfig(config.csconfig)
	if err != nil {
		return fmt.Errorf("Failed to create leaderelection client: %v", err)
	}

	le := leaderelection.New(clientset, lockName,
		func(ctx context.Context) {
			StartController(config)
		},
		leaderelection.LeaseDuration(*config.flags.leaderElectionLeaseDuration),
		leaderelection.RenewDeadline(*config.flags.leaderElectionRenewDeadline),
		leaderelection.RetryPeriod(*config.flags.leaderElectionRetryPeriod),
		leaderelection.Namespace(*config.flags.leaderElectionNamespace),
	)

	if *config.flags.httpEndpoint != "" {
		le.PrepareHealthCheck(config.mux)
	}

	if err := le.Run(); err != nil {
		return fmt.Errorf("leader election failed: %v", err)
	}

	return nil
}

func StartController(config *config_t) {
	klog.V(3).Infof("Starting controller without leader election")
	driver := newDriver(config)
	informerFactory := informers.NewSharedInformerFactory(config.clientset.core, 0 /* resync period */)
	ctrl := controller.New(config.ctx, mycrd.ApiGroupName, driver, config.clientset.core, informerFactory)
	informerFactory.Start(config.ctx.Done())
	ctrl.Run(*config.flags.workers)
}
