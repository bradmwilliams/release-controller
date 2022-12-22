package release_payload_controller

import (
	"context"
	imageclient "github.com/openshift/client-go/image/clientset/versioned"
	imageinformers "github.com/openshift/client-go/image/informers/externalversions"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	releasepayloadclient "github.com/openshift/release-controller/pkg/client/clientset/versioned"
	releasepayloadinformers "github.com/openshift/release-controller/pkg/client/informers/externalversions"
	"github.com/openshift/release-controller/pkg/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"
)

type Options struct {
	controllerContext *controllercmd.ControllerContext
}

func NewReleasePayloadSyncCommand(name string) *cobra.Command {
	o := &Options{}

	ccc := controllercmd.NewControllerCommandConfig("release-payload-sync-controller", version.Get(), func(ctx context.Context, controllerContext *controllercmd.ControllerContext) error {
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
	})

	// Turn off leader election for local execution...
	ccc.DisableLeaderElection = true

	cmd := ccc.NewCommandWithContext(context.Background())
	cmd.Use = name
	cmd.Short = "Start the release payload sync controller"

	o.AddFlags(cmd.Flags())

	return cmd
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
}

func (o *Options) Validate(ctx context.Context) error {
	return nil
}

func (o *Options) Run(ctx context.Context) error {
	inClusterConfig := o.controllerContext.KubeConfig

	// ImageStream Informers
	imageClient, err := imageclient.NewForConfig(inClusterConfig)
	if err != nil {
		klog.Fatalf("Error building image clientset: %s", err.Error())
	}
	imageInformerFactory := imageinformers.NewSharedInformerFactory(imageClient, controllerDefaultResyncDuration)
	imagestreamInformer := imageInformerFactory.Image().V1().ImageStreams()

	// ReleasePayload Informers
	releasePayloadClient, err := releasepayloadclient.NewForConfig(inClusterConfig)
	if err != nil {
		klog.Fatalf("Error building releasePayload clientset: %s", err.Error())
	}
	releasePayloadInformerFactory := releasepayloadinformers.NewSharedInformerFactory(releasePayloadClient, controllerDefaultResyncDuration)
	releasePayloadInformer := releasePayloadInformerFactory.Release().V1alpha1().ReleasePayloads()

	// Imagestream Controller
	imagestreamController, err := NewImagestreamController(releasePayloadInformer, releasePayloadClient.ReleaseV1alpha1(), imagestreamInformer, o.controllerContext.EventRecorder)
	if err != nil {
		return err
	}

	// Start the informers
	imageInformerFactory.Start(ctx.Done())
	releasePayloadInformerFactory.Start(ctx.Done())

	// Run the Controllers
	go imagestreamController.RunWorkers(ctx, 1)

	<-ctx.Done()

	return nil
}
