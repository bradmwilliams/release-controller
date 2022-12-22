package release_payload_controller

import (
	"context"
	"encoding/json"
	"fmt"
	v1 "github.com/openshift/api/image/v1"
	imagev1informer "github.com/openshift/client-go/image/informers/externalversions/image/v1"
	imagev1lister "github.com/openshift/client-go/image/listers/image/v1"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/release-controller/pkg/apis/release/v1alpha1"
	releasepayloadclient "github.com/openshift/release-controller/pkg/client/clientset/versioned/typed/release/v1alpha1"
	releasepayloadinformer "github.com/openshift/release-controller/pkg/client/informers/externalversions/release/v1alpha1"
	releasecontroller "github.com/openshift/release-controller/pkg/release-controller"
	"k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"strings"
)

// ImagestreamController is responsible for watching Imagestreams, in the release-namespace
type ImagestreamController struct {
	*ReleasePayloadController

	imagestreamLister imagev1lister.ImageStreamLister
}

func NewImagestreamController(
	releasePayloadInformer releasepayloadinformer.ReleasePayloadInformer,
	releasePayloadClient releasepayloadclient.ReleaseV1alpha1Interface,
	imagestreamInformer imagev1informer.ImageStreamInformer,
	eventRecorder events.Recorder,
) (*ImagestreamController, error) {
	c := &ImagestreamController{
		ReleasePayloadController: NewReleasePayloadController("Imagestream Controller",
			releasePayloadInformer,
			releasePayloadClient,
			eventRecorder.WithComponentSuffix("imagestream-controller"),
			workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "ImagestreamController")),
		imagestreamLister: imagestreamInformer.Lister(),
	}

	c.syncFn = c.sync
	c.cachesToSync = append(c.cachesToSync, imagestreamInformer.Informer().HasSynced)

	imagestreamFilter := func(obj interface{}) bool {
		if imagestream, ok := obj.(*v1.ImageStream); ok {
			if _, ok := imagestream.Annotations[releasecontroller.ReleaseAnnotationHasReleases]; ok {
				if _, ok := imagestream.Annotations[releasecontroller.ReleaseAnnotationConfig]; ok {
					return true
				}
			}
		}
		return false
	}

	imagestreamInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: imagestreamFilter,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    c.Enqueue,
			UpdateFunc: func(old, new interface{}) { c.Enqueue(new) },
			DeleteFunc: c.Enqueue,
		},
	})

	return c, nil
}

var verificationStatusMap = map[string]string{
	"Pending":   "Pending",
	"Succeeded": "Success",
	"Failed":    "Failure",
}

type releasePayloadUpdate struct {
	results []v1alpha1.JobRunResult
	state   v1alpha1.JobState
}

func (c *ImagestreamController) sync(ctx context.Context, key string) error {
	klog.V(4).Infof("Starting ImagestreamController sync")
	defer klog.V(4).Infof("ImagestreamController sync done")

	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	klog.V(4).Infof("Processing Imagestream: '%s/%s' from workQueue", namespace, name)

	// Get the Imagestream resource with this namespace/name
	originalImagestream, err := c.imagestreamLister.ImageStreams(namespace).Get(name)
	// The Imagestream resource may no longer exist, in which case we stop processing.
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	var problems []string

	for _, tag := range originalImagestream.Spec.Tags {
		// Get the ReleasePayload resource with this namespace/name
		originalPayload, err := c.releasePayloadLister.ReleasePayloads(namespace).Get(tag.Name)
		// The ReleasePayload resource may no longer exist, in which case we stop processing.
		if errors.IsNotFound(err) {
			klog.Warningf("ReleasePayload: %q does not exist", tag.Name)
			problems = append(problems, tag.Name)
			continue
		}
		if err != nil {
			return err
		}

		var verificationStatus releasecontroller.VerificationStatusMap

		phase := tag.Annotations[releasecontroller.ReleaseAnnotationPhase]
		if len(phase) == 0 {
			klog.Warningf("Tag: %q does not contain any phase information", tag.Name)
			problems = append(problems, tag.Name)
			continue
		}

		klog.V(4).Infof("Processing %q tag: %q", phase, tag.Name)

		results := tag.Annotations[releasecontroller.ReleaseAnnotationVerify]
		if len(results) == 0 {
			klog.Warningf("Tag: %q does not contain any verification results", tag.Name)
			problems = append(problems, tag.Name)
			continue
		}

		if err := json.Unmarshal([]byte(results), &verificationStatus); err != nil {
			return fmt.Errorf("unable to unmarshal verification results for tag: %q", tag.Name)
		}

		klog.V(4).Infof("Found: %d results for tag: %q", len(verificationStatus), tag.Name)

		updates := make(map[string]releasePayloadUpdate)

		for k, v := range verificationStatus {
			jobStatus := lookupJobStatus(originalPayload, k)
			if jobStatus == nil {
				continue
			}

			if string(jobStatus.AggregateState) != verificationStatusMap[v.State] {
				klog.Warningf("** Payload (%s) and VerificationStatus (%s) mismatch **", jobStatus.AggregateState, verificationStatusMap[v.State])
			}

			klog.V(4).Infof(" - Found: %s", jobStatus.CIConfigurationName)

			if jobStatus.JobRunResults == nil || len(jobStatus.JobRunResults) == 0 {
				klog.V(4).Infof(" - No JobRunResults")
				updates[k] = releasePayloadUpdate{results: []v1alpha1.JobRunResult{
					{
						State:               getJobRunState(v.State),
						HumanProwResultsURL: v.URL,
					},
				}}
			} else {
				if string(jobStatus.AggregateState) != verificationStatusMap[v.State] {
					klog.V(4).Infof(" -- Need to update JobRunResults")
					klog.V(4).Infof(" -- Fix: %s -> %s", jobStatus.AggregateState, v.State)
					klog.V(4).Infof(" -- URL: %s", v.URL)

					updates[k] = releasePayloadUpdate{state: "FIX_ME"}
				}
			}
		}

		if len(updates) > 0 {
			klog.V(4).Infof("Updating ReleasePayload: %s -> [%d]", originalPayload.Name, len(updates))
		}
	}

	return nil
}

func lookupJobStatus(payload *v1alpha1.ReleasePayload, name string) *v1alpha1.JobStatus {
	klog.V(4).Infof("Looking for: %q in payload: %s", name, payload.Name)

	for _, v := range payload.Status.InformingJobResults {
		if v.CIConfigurationName == name && !strings.HasPrefix(name, "aggregated-") {
			return &v
		}
	}
	for _, v := range payload.Status.BlockingJobResults {
		if v.CIConfigurationName == name {
			return &v
		}
	}
	return nil
}

func getJobRunState(status string) v1alpha1.JobRunState {
	switch status {
	case releasecontroller.ReleaseVerificationStatePending:
		return v1alpha1.JobRunStatePending
	case releasecontroller.ReleaseVerificationStateFailed:
		return v1alpha1.JobRunStateFailure
	case releasecontroller.ReleaseVerificationStateSucceeded:
		return v1alpha1.JobRunStateSuccess
	default:
		return v1alpha1.JobRunStateUnknown
	}
}
