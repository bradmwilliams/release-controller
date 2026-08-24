package release_reimport_controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	imageclientset "github.com/openshift/client-go/image/clientset/versioned"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/release-controller/pkg/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

type Options struct {
	controllerContext  *controllercmd.ControllerContext
	namespaces         []string
	dryRun             bool
	ReleasesKubeconfig string
}

func NewReleaseReimportControllerCommand(name string) *cobra.Command {
	o := &Options{}

	ccc := controllercmd.NewControllerCommandConfig("release-reimport-controller", version.Get(), func(ctx context.Context, controllerContext *controllercmd.ControllerContext) error {
		o.controllerContext = controllerContext

		err := o.Validate(ctx)
		if err != nil {
			return err
		}

		err = o.Run(ctx)
		if err != nil {
			return err
		}

		return nil
	}, clock.RealClock{})
	ccc.DisableLeaderElection = true

	cmd := ccc.NewCommandWithContext(context.Background())
	cmd.Use = name
	cmd.Short = "Start the release reimport controller"

	o.AddFlags(cmd.Flags())

	return cmd
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringArrayVar(&o.namespaces, "namespaces", []string{}, "Namespaces to watch for automatic reimporting")
	fs.BoolVar(&o.dryRun, "dry-run", false, "Run 'oc import-image' commands in dry-run mode")
	fs.StringVar(&o.ReleasesKubeconfig, "releases-kubeconfig", o.ReleasesKubeconfig, "The kubeconfig to use for interacting with release imagestreams. Falls back to in-cluster config if unset.")
}

func (o *Options) Validate(ctx context.Context) error {
	if len(o.namespaces) == 0 {
		return errors.New("--namespaces flag must be set")
	}
	return nil
}

func (o *Options) Run(ctx context.Context) error {
	inClusterConfig := o.controllerContext.KubeConfig

	releasesCfg, err := resolveKubeconfig(o.ReleasesKubeconfig, inClusterConfig)
	if err != nil {
		return fmt.Errorf("failed to load releases kubeconfig: %w", err)
	}

	// ImageStream Informers
	imageStreamClient, err := imageclientset.NewForConfig(releasesCfg)
	if err != nil {
		klog.Fatalf("Error building imagestream clientset: %s", err.Error())
	}

	imageReimportController := NewImageReimportController(imageStreamClient, o.namespaces, o.dryRun)

	go imageReimportController.Run(ctx, 10*time.Minute)

	<-ctx.Done()

	return nil
}

// resolveKubeconfig loads a kubeconfig from path, or returns the fallback config if path is empty.
func resolveKubeconfig(path string, fallback *rest.Config) (*rest.Config, error) {
	if path == "" {
		return fallback, nil
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: path},
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
}
