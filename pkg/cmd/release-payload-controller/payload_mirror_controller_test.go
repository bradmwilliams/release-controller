package release_payload_controller

import (
	"context"
	"fmt"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"github.com/openshift/release-controller/pkg/apis/release/v1alpha1"
	"github.com/openshift/release-controller/pkg/client/clientset/versioned/fake"
	releasepayloadinformers "github.com/openshift/release-controller/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"testing"
)

func TestPayloadMirrorSync(t *testing.T) {
	testCases := []struct {
		name     string
		payload  *v1alpha1.ReleasePayload
		expected *v1alpha1.ReleasePayload
	}{
		{
			name: "ReleasePayloadWithoutReleaseMirrorJobStatusOrConditions",
			payload: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Status: v1alpha1.ReleasePayloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:   v1alpha1.ConditionPayloadMirrorFailed,
							Status: metav1.ConditionUnknown,
							Reason: ReleasePayloadMirrorFailedReason,
						},
						{
							Type:   v1alpha1.ConditionPayloadMirrored,
							Status: metav1.ConditionUnknown,
							Reason: ReleasePayloadMirroredReason,
						},
					},
				},
			},
		},
		{
			name: "ReleasePayloadWithSuccessfulConditions",
			payload: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Status: v1alpha1.ReleasePayloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:    v1alpha1.ConditionPayloadMirrored,
							Status:  metav1.ConditionTrue,
							Reason:  ReleasePayloadMirroredReason,
							Message: ReleaseMirrorJobSuccessMessage,
						},
						{
							Type:    v1alpha1.ConditionPayloadMirrorFailed,
							Status:  metav1.ConditionFalse,
							Reason:  ReleasePayloadMirrorFailedReason,
							Message: ReleaseMirrorJobSuccessMessage,
						},
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Status: v1alpha1.ReleasePayloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:    v1alpha1.ConditionPayloadMirrored,
							Status:  metav1.ConditionTrue,
							Reason:  ReleasePayloadMirroredReason,
							Message: ReleaseMirrorJobSuccessMessage,
						},
						{
							Type:    v1alpha1.ConditionPayloadMirrorFailed,
							Status:  metav1.ConditionFalse,
							Reason:  ReleasePayloadMirrorFailedReason,
							Message: ReleaseMirrorJobSuccessMessage,
						},
					},
				},
			},
		},
		{
			name: "ReleasePayloadWithFailureConditions",
			payload: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Status: v1alpha1.ReleasePayloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:    v1alpha1.ConditionPayloadMirrored,
							Status:  metav1.ConditionFalse,
							Reason:  ReleasePayloadMirroredReason,
							Message: ReleaseMirrorJobFailureMessage,
						},
						{
							Type:    v1alpha1.ConditionPayloadMirrorFailed,
							Status:  metav1.ConditionTrue,
							Reason:  ReleasePayloadMirrorFailedReason,
							Message: ReleaseMirrorJobFailureMessage,
						},
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Status: v1alpha1.ReleasePayloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:    v1alpha1.ConditionPayloadMirrored,
							Status:  metav1.ConditionFalse,
							Reason:  ReleasePayloadMirroredReason,
							Message: ReleaseMirrorJobFailureMessage,
						},
						{
							Type:    v1alpha1.ConditionPayloadMirrorFailed,
							Status:  metav1.ConditionTrue,
							Reason:  ReleasePayloadMirrorFailedReason,
							Message: ReleaseMirrorJobFailureMessage,
						},
					},
				},
			},
		},
		{
			name: "ReleasePayloadWithMixedConditions",
			payload: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Status: v1alpha1.ReleasePayloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:    v1alpha1.ConditionPayloadMirrored,
							Status:  metav1.ConditionTrue,
							Reason:  ReleasePayloadMirroredReason,
							Message: ReleaseMirrorJobSuccessMessage,
						},
						{
							Type:    v1alpha1.ConditionPayloadMirrorFailed,
							Status:  metav1.ConditionUnknown,
							Reason:  ReleasePayloadMirrorFailedReason,
							Message: ReleaseMirrorJobFailureMessage,
						},
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Status: v1alpha1.ReleasePayloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:   v1alpha1.ConditionPayloadMirrorFailed,
							Status: metav1.ConditionUnknown,
							Reason: ReleasePayloadMirrorFailedReason,
						},
						{
							Type:   v1alpha1.ConditionPayloadMirrored,
							Status: metav1.ConditionUnknown,
							Reason: ReleasePayloadMirroredReason,
						},
					},
				},
			},
		},
		{
			name: "ReleasePayloadWithSuccessfulReleaseMirrorJob",
			payload: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Status: v1alpha1.ReleasePayloadStatus{
					ReleaseMirrorJobResult: v1alpha1.ReleaseMirrorJobResult{
						Status: v1alpha1.ReleaseMirrorJobSuccess,
					},
					Conditions: []metav1.Condition{
						{
							Type:   v1alpha1.ConditionPayloadMirrored,
							Status: metav1.ConditionUnknown,
							Reason: ReleasePayloadMirroredReason,
						},
						{
							Type:   v1alpha1.ConditionPayloadMirrorFailed,
							Status: metav1.ConditionUnknown,
							Reason: ReleasePayloadMirrorFailedReason,
						},
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Status: v1alpha1.ReleasePayloadStatus{
					ReleaseMirrorJobResult: v1alpha1.ReleaseMirrorJobResult{
						Status: v1alpha1.ReleaseMirrorJobSuccess,
					},
					Conditions: []metav1.Condition{
						{
							Type:    v1alpha1.ConditionPayloadMirrorFailed,
							Status:  metav1.ConditionFalse,
							Reason:  ReleasePayloadMirrorFailedReason,
							Message: ReleaseMirrorJobSuccessMessage,
						},
						{
							Type:    v1alpha1.ConditionPayloadMirrored,
							Status:  metav1.ConditionTrue,
							Reason:  ReleasePayloadMirroredReason,
							Message: ReleaseMirrorJobSuccessMessage,
						},
					},
				},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			releasePayloadClient := fake.NewSimpleClientset(testCase.payload)
			releasePayloadInformerFactory := releasepayloadinformers.NewSharedInformerFactory(releasePayloadClient, controllerDefaultResyncDuration)
			releasePayloadInformer := releasePayloadInformerFactory.Release().V1alpha1().ReleasePayloads()

			c := &PayloadMirrorController{
				ReleasePayloadController: NewReleasePayloadController("Payload Mirror Controller",
					releasePayloadInformer,
					releasePayloadClient.ReleaseV1alpha1(),
					events.NewInMemoryRecorder("payload-mirror-controller-test"),
					workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "ReleaseMirrorJobController"})),
			}

			releasePayloadFilter := func(obj interface{}) bool {
				if releasePayload, ok := obj.(*v1alpha1.ReleasePayload); ok {
					// If the conditions are both in their respective terminal states, then there is nothing else to do...
					if (v1helpers.IsConditionTrue(releasePayload.Status.Conditions, v1alpha1.ConditionPayloadMirrored) ||
						v1helpers.IsConditionFalse(releasePayload.Status.Conditions, v1alpha1.ConditionPayloadMirrored)) &&
						(v1helpers.IsConditionTrue(releasePayload.Status.Conditions, v1alpha1.ConditionPayloadMirrorFailed) ||
							v1helpers.IsConditionFalse(releasePayload.Status.Conditions, v1alpha1.ConditionPayloadMirrorFailed)) {
						return false
					}
					return true
				}
				return false
			}

			releasePayloadInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
				FilterFunc: releasePayloadFilter,
				Handler: cache.ResourceEventHandlerFuncs{
					AddFunc:    c.Enqueue,
					UpdateFunc: func(old, new interface{}) { c.Enqueue(new) },
					DeleteFunc: c.Enqueue,
				},
			})

			releasePayloadInformerFactory.Start(context.Background().Done())

			if !cache.WaitForNamedCacheSync("ReleaseMirrorJobController", context.Background().Done(), c.cachesToSync...) {
				t.Errorf("%s: error waiting for caches to sync", testCase.name)
				return
			}

			err := c.sync(context.TODO(), fmt.Sprintf("%s/%s", testCase.payload.Namespace, testCase.payload.Name))
			if err != nil {
				t.Errorf("%s: unexpected err: %v", testCase.name, err)
			}

			// Performing a live lookup instead of having to wait for the cache to sink (again)...
			output, err := c.releasePayloadClient.ReleasePayloads(testCase.payload.Namespace).Get(context.TODO(), testCase.payload.Name, metav1.GetOptions{})
			if !cmp.Equal(output, testCase.expected, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")) {
				t.Errorf("%s: Expected %v, got %v", testCase.name, testCase.expected, output)
			}
		})
	}
}