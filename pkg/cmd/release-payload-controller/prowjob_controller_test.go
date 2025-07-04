package release_payload_controller

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/release-controller/pkg/apis/release/v1alpha1"
	"github.com/openshift/release-controller/pkg/client/clientset/versioned/fake"
	releasepayloadinformers "github.com/openshift/release-controller/pkg/client/informers/externalversions"
	releasecontroller "github.com/openshift/release-controller/pkg/release-controller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
	v1 "sigs.k8s.io/prow/pkg/apis/prowjobs/v1"
	prowfake "sigs.k8s.io/prow/pkg/client/clientset/versioned/fake"
	prowjobinformers "sigs.k8s.io/prow/pkg/client/informers/externalversions"
	"sigs.k8s.io/prow/pkg/kube"
)

func newJobStatus(name, jobName string, maxRetries, analysisJobCount int, aggregateState v1alpha1.JobState, results []v1alpha1.JobRunResult) v1alpha1.JobStatus {
	return v1alpha1.JobStatus{
		CIConfigurationName:    name,
		CIConfigurationJobName: jobName,
		MaxRetries:             maxRetries,
		AnalysisJobCount:       analysisJobCount,
		AggregateState:         aggregateState,
		JobRunResults:          results,
	}
}

func newJobStatusPointer(name, jobName string, maxRetries, analysisJobCount int, aggregateState v1alpha1.JobState, results []v1alpha1.JobRunResult) *v1alpha1.JobStatus {
	status := newJobStatus(name, jobName, maxRetries, analysisJobCount, aggregateState, results)
	return &status
}

func newJobRunResult(name, namespace, cluster string, state v1alpha1.JobRunState, url string, upgrade v1alpha1.JobRunUpgradeType) v1alpha1.JobRunResult {
	return v1alpha1.JobRunResult{
		Coordinates: v1alpha1.JobRunCoordinates{
			Name:      name,
			Namespace: namespace,
			Cluster:   cluster,
		},
		State:               state,
		HumanProwResultsURL: url,
		UpgradeType:         upgrade,
	}
}

func newProwJob(name, namespace, release, jobName, source, cluster string, state v1.ProwJobState, url string) *v1.ProwJob {
	return &v1.ProwJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				releasecontroller.ReleaseAnnotationVerify: "true",
				releasecontroller.ReleaseLabelPayload:     release,
			},
			Annotations: map[string]string{
				kube.ProwJobAnnotation:                    jobName,
				releasecontroller.ReleaseAnnotationSource: source,
				releasecontroller.ReleaseAnnotationToTag:  release,
			},
		},
		Spec: v1.ProwJobSpec{
			Cluster: cluster,
		},
		Status: v1.ProwJobStatus{
			State: state,
			URL:   url,
		},
	}
}

func TestSetJobStatus(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		input     *v1alpha1.ReleasePayloadStatus
		jobStatus v1alpha1.JobStatus
		expected  *v1alpha1.ReleasePayloadStatus
	}{
		{
			name: "NoMatchingJob",
			input: &v1alpha1.ReleasePayloadStatus{
				BlockingJobResults: []v1alpha1.JobStatus{},
			},
			jobStatus: newJobStatus("A", "B", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{}),
			expected: &v1alpha1.ReleasePayloadStatus{
				BlockingJobResults: []v1alpha1.JobStatus{},
			},
		},
		{
			name: "MatchingBlockingJobUpdated",
			input: &v1alpha1.ReleasePayloadStatus{
				BlockingJobResults: []v1alpha1.JobStatus{
					newJobStatus("A", "B", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{}),
				},
			},
			jobStatus: newJobStatus("A", "B", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
				newJobRunResult("X", "Y", "Z", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
			}),
			expected: &v1alpha1.ReleasePayloadStatus{
				BlockingJobResults: []v1alpha1.JobStatus{
					newJobStatus("A", "B", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
						newJobRunResult("X", "Y", "Z", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
					}),
				},
			},
		},
		{
			name: "MatchingInformingJobUpdated",
			input: &v1alpha1.ReleasePayloadStatus{
				InformingJobResults: []v1alpha1.JobStatus{
					newJobStatus("A", "B", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{}),
				},
			},
			jobStatus: newJobStatus("A", "B", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
				newJobRunResult("X", "Y", "Z", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
			}),
			expected: &v1alpha1.ReleasePayloadStatus{
				InformingJobResults: []v1alpha1.JobStatus{
					newJobStatus("A", "B", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
						newJobRunResult("X", "Y", "Z", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
					}),
				},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			setJobStatus(testCase.input, testCase.jobStatus)
			if !reflect.DeepEqual(testCase.input, testCase.expected) {
				t.Errorf("%s: Expected %v, got %v", testCase.name, testCase.expected, testCase.input)
			}
		})
	}
}

func TestFindJobStatus(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                   string
		payloadName            string
		input                  *v1alpha1.ReleasePayloadStatus
		ciConfigurationName    string
		ciConfigurationJobName string
		expected               *v1alpha1.JobStatus
		expectedError          string
	}{
		{
			name:                   "NoJobs",
			payloadName:            "4.11.0-0.nightly-2022-06-23-153912",
			input:                  &v1alpha1.ReleasePayloadStatus{},
			ciConfigurationName:    "A",
			ciConfigurationJobName: "B",
			expected:               nil,
			expectedError:          "unable to locate job results for A (B)",
		},
		{
			name:        "BlockingJob",
			payloadName: "4.11.0-0.nightly-2022-06-23-153912",
			input: &v1alpha1.ReleasePayloadStatus{
				BlockingJobResults: []v1alpha1.JobStatus{
					newJobStatus("A", "B", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
						newJobRunResult("X", "Y", "Z", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
					}),
				},
			},
			ciConfigurationName:    "A",
			ciConfigurationJobName: "B",
			expected: newJobStatusPointer("A", "B", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
				newJobRunResult("X", "Y", "Z", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
			}),
		},
		{
			name:        "InformingJob",
			payloadName: "4.11.0-0.nightly-2022-06-23-153912",
			input: &v1alpha1.ReleasePayloadStatus{
				InformingJobResults: []v1alpha1.JobStatus{
					newJobStatus("A", "B", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
						newJobRunResult("X", "Y", "Z", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
					}),
				},
			},
			ciConfigurationName:    "A",
			ciConfigurationJobName: "B",
			expected: newJobStatusPointer("A", "B", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
				newJobRunResult("X", "Y", "Z", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
			}),
		},
		{
			name:        "NoMatchingJob",
			payloadName: "4.11.0-0.nightly-2022-06-23-153912",
			input: &v1alpha1.ReleasePayloadStatus{
				BlockingJobResults: []v1alpha1.JobStatus{
					{
						CIConfigurationName:    "A",
						CIConfigurationJobName: "B",
					},
				},
				InformingJobResults: []v1alpha1.JobStatus{
					{
						CIConfigurationName:    "C",
						CIConfigurationJobName: "D",
					},
				},
			},
			ciConfigurationName:    "X",
			ciConfigurationJobName: "Y",
			expected:               nil,
			expectedError:          "unable to locate job results for X (Y)",
		},
		{
			name:        "AutomaticReleaseUpgradeTest",
			payloadName: "4.11.22",
			input: &v1alpha1.ReleasePayloadStatus{
				UpgradeJobResults: []v1alpha1.JobStatus{
					newJobStatus("aws", "release-openshift-origin-installer-e2e-aws-upgrade", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
						newJobRunResult("4.11.22-upgrade-from-4.10.18-aws", "ci", "build02", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
					}),
				},
			},
			ciConfigurationName:    "aws",
			ciConfigurationJobName: "release-openshift-origin-installer-e2e-aws-upgrade",
			expected: newJobStatusPointer("aws", "release-openshift-origin-installer-e2e-aws-upgrade", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
				newJobRunResult("4.11.22-upgrade-from-4.10.18-aws", "ci", "build02", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
			}),
		},
		{
			name:        "OKD-SCOSNextBlockingJob",
			payloadName: "4.20.0-okd-scos.ec.5",
			input: &v1alpha1.ReleasePayloadStatus{
				BlockingJobResults: []v1alpha1.JobStatus{
					newJobStatus("upgrade", "release-openshift-okd-scos-installer-e2e-aws-upgrade-from-scos-next", 2, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
						newJobRunResult("4.19.0-okd-scos.ec.5-upgrade", "ci", "build02", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
					}),
				},
			},
			ciConfigurationName:    "upgrade",
			ciConfigurationJobName: "release-openshift-okd-scos-installer-e2e-aws-upgrade-from-scos-next",
			expected: newJobStatusPointer("upgrade", "release-openshift-okd-scos-installer-e2e-aws-upgrade-from-scos-next", 2, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
				newJobRunResult("4.19.0-okd-scos.ec.5-upgrade", "ci", "build02", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
			}),
		},
		{
			name:        "OKD-SCOS-StableBlockingJob",
			payloadName: "4.19.0-okd-scos.5",
			input: &v1alpha1.ReleasePayloadStatus{
				BlockingJobResults: []v1alpha1.JobStatus{
					newJobStatus("upgrade", "release-openshift-okd-scos-installer-e2e-aws-upgrade-from-scos-stable", 2, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
						newJobRunResult("4.19.0-okd-scos.5-upgrade", "ci", "build02", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
					}),
				},
			},
			ciConfigurationName:    "upgrade",
			ciConfigurationJobName: "release-openshift-okd-scos-installer-e2e-aws-upgrade-from-scos-stable",
			expected: newJobStatusPointer("upgrade", "release-openshift-okd-scos-installer-e2e-aws-upgrade-from-scos-stable", 2, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
				newJobRunResult("4.19.0-okd-scos.5-upgrade", "ci", "build02", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
			}),
		},
		{
			name:        "OKD-4-StableBlockingJob",
			payloadName: "4.15.0-0.okd-2024-03-10-010116",
			input: &v1alpha1.ReleasePayloadStatus{
				BlockingJobResults: []v1alpha1.JobStatus{
					newJobStatus("upgrade", "release-openshift-okd-scos-installer-e2e-aws-upgrade", 2, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
						newJobRunResult("4.15.0-0.okd-2024-03-10-010116-upgrade", "ci", "build02", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
					}),
				},
			},
			ciConfigurationName:    "upgrade",
			ciConfigurationJobName: "release-openshift-okd-scos-installer-e2e-aws-upgrade",
			expected: newJobStatusPointer("upgrade", "release-openshift-okd-scos-installer-e2e-aws-upgrade", 2, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
				newJobRunResult("4.15.0-0.okd-2024-03-10-010116-upgrade", "ci", "build02", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
			}),
		},
		{
			name:        "OKD-SCOSBlockingJob",
			payloadName: "4.20.0-0.okd-scos-2025-06-30-052314",
			input: &v1alpha1.ReleasePayloadStatus{
				BlockingJobResults: []v1alpha1.JobStatus{
					newJobStatus("aws", "periodic-ci-openshift-release-master-okd-scos-4.20-e2e-aws-ovn", 2, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
						newJobRunResult("4.20.0-0.okd-scos-2025-06-30-052314-aws", "ci", "build02", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
					}),
				},
			},
			ciConfigurationName:    "aws",
			ciConfigurationJobName: "periodic-ci-openshift-release-master-okd-scos-4.20-e2e-aws-ovn",
			expected: newJobStatusPointer("aws", "periodic-ci-openshift-release-master-okd-scos-4.20-e2e-aws-ovn", 2, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
				newJobRunResult("4.20.0-0.okd-scos-2025-06-30-052314-aws", "ci", "build02", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
			}),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			jobStatus, err := findJobStatus(testCase.payloadName, testCase.input, testCase.ciConfigurationName, testCase.ciConfigurationJobName)
			if err != nil && !cmp.Equal(err.Error(), testCase.expectedError) {
				t.Fatalf("%s: Expected error %v, got %v", testCase.name, testCase.expectedError, err)
			}
			if !reflect.DeepEqual(jobStatus, testCase.expected) {
				t.Errorf("%s: Expected %v, got %v", testCase.name, testCase.expected, jobStatus)
			}
		})
	}
}

func TestProwJobStatusSync(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		prowjob     []runtime.Object
		input       *v1alpha1.ReleasePayload
		expected    *v1alpha1.ReleasePayload
		expectedErr error
	}{
		{
			name: "MissingJobStatus",
			prowjob: []runtime.Object{
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aws-serial", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", "ocp/4.11-art-latest", "build01", v1.SuccessState, "https://abc.123.com"),
			},
			input: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{},
				},
			},
		},
		{
			name: "SingleProwJob",
			prowjob: []runtime.Object{
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aws-serial", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", "ocp/4.11-art-latest", "build01", v1.SuccessState, "https://abc.123.com"),
			},
			input: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws-serial", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", 0, 0, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws-serial", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aws-serial", "ci", "build01", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
						}),
					},
				},
			},
		},
		{
			name: "MultipleProwJobs",
			prowjob: []runtime.Object{
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aws-serial", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", "ocp/4.11-art-latest", "build01", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aws-single-node", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-single-node", "ocp/4.11-art-latest", "build02", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aws-techpreview", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-techpreview", "ocp/4.11-art-latest", "build01", v1.SuccessState, "https://abc.123.com"),
			},
			input: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws-serial", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", 0, 0, v1alpha1.JobStateUnknown, nil),
						newJobStatus("aws-single-node", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-single-node", 0, 0, v1alpha1.JobStateUnknown, nil),
						newJobStatus("aws-techpreview", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-techpreview", 0, 0, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws-serial", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aws-serial", "ci", "build01", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
						}),
						newJobStatus("aws-single-node", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-single-node", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aws-single-node", "ci", "build02", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
						}),
						newJobStatus("aws-techpreview", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-techpreview", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aws-techpreview", "ci", "build01", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
						}),
					},
				},
			},
		},
		{
			name: "RetriedProwJob",
			prowjob: []runtime.Object{
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aws-serial", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", "ocp/4.11-art-latest", "build01", v1.FailureState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aws-serial-1", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", "ocp/4.11-art-latest", "build02", v1.FailureState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aws-serial-2", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", "ocp/4.11-art-latest", "build01", v1.PendingState, "https://abc.123.com"),
			},
			input: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws-serial", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", 3, 0, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws-serial", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", 3, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aws-serial", "ci", "build01", v1alpha1.JobRunStateFailure, "https://abc.123.com", ""),
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aws-serial-1", "ci", "build02", v1alpha1.JobRunStateFailure, "https://abc.123.com", ""),
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aws-serial-2", "ci", "build01", v1alpha1.JobRunStatePending, "https://abc.123.com", ""),
						}),
					},
				},
			},
		},
		{
			name: "AnalysisProwJobs",
			prowjob: []runtime.Object{
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-aggregator", "ci", "4.11.0-0.nightly-2022-02-09-091559", "aggregated-azure-ovn-upgrade-4.11-micro-release-openshift-release-analysis-aggregator", "ocp/4.11-art-latest", "build01", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-0", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-ci-4.11-e2e-azure-ovn-upgrade", "ocp/4.11-art-latest", "build01", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-1", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-ci-4.11-e2e-azure-ovn-upgrade", "ocp/4.11-art-latest", "build02", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-2", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-ci-4.11-e2e-azure-ovn-upgrade", "ocp/4.11-art-latest", "build01", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-3", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-ci-4.11-e2e-azure-ovn-upgrade", "ocp/4.11-art-latest", "build03", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-4", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-ci-4.11-e2e-azure-ovn-upgrade", "ocp/4.11-art-latest", "build02", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-5", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-ci-4.11-e2e-azure-ovn-upgrade", "ocp/4.11-art-latest", "build05", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-6", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-ci-4.11-e2e-azure-ovn-upgrade", "ocp/4.11-art-latest", "build01", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-7", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-ci-4.11-e2e-azure-ovn-upgrade", "ocp/4.11-art-latest", "build04", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-8", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-ci-4.11-e2e-azure-ovn-upgrade", "ocp/4.11-art-latest", "build02", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-9", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-ci-4.11-e2e-azure-ovn-upgrade", "ocp/4.11-art-latest", "build01", v1.SuccessState, "https://abc.123.com"),
			},
			input: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aggregated-azure-ovn-upgrade-4.11-micro", "aggregated-azure-ovn-upgrade-4.11-micro-release-openshift-release-analysis-aggregator", 0, 0, v1alpha1.JobStateUnknown, nil),
					},
					InformingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aggregated-azure-ovn-upgrade-4.11-micro", "periodic-ci-openshift-release-master-ci-4.11-e2e-azure-ovn-upgrade", 0, 10, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aggregated-azure-ovn-upgrade-4.11-micro", "aggregated-azure-ovn-upgrade-4.11-micro-release-openshift-release-analysis-aggregator", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-aggregator", "ci", "build01", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
						}),
					},
					InformingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aggregated-azure-ovn-upgrade-4.11-micro", "periodic-ci-openshift-release-master-ci-4.11-e2e-azure-ovn-upgrade", 0, 10, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-0", "ci", "build01", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-1", "ci", "build02", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-2", "ci", "build01", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-3", "ci", "build03", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-4", "ci", "build02", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-5", "ci", "build05", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-6", "ci", "build01", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-7", "ci", "build04", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-8", "ci", "build02", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
							newJobRunResult("4.11.0-0.nightly-2022-02-09-091559-aggregate-b99pz22-analysis-9", "ci", "build01", v1alpha1.JobRunStateSuccess, "https://abc.123.com", ""),
						}),
					},
				},
			},
		},
		{
			name: "AutomatedUpgradeJobs",
			prowjob: []runtime.Object{
				newProwJob("4.11.22-upgrade-from-4.10.18-aws", "ci", "4.11.22", "release-openshift-origin-installer-e2e-aws-upgrade", "ocp/release", "build01", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.22-upgrade-from-4.10.44-azure", "ci", "4.11.22", "release-openshift-origin-installer-e2e-azure-upgrade", "ocp/release", "build02", v1.SuccessState, "https://abc.123.com"),
				newProwJob("4.11.22-upgrade-from-4.11.10-gcp", "ci", "4.11.22", "release-openshift-origin-installer-e2e-gcp-upgrade", "ocp/release", "build03", v1.SuccessState, "https://abc.123.com"),
			},
			input: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.22",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					UpgradeJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws", "release-openshift-origin-installer-e2e-aws-upgrade", 0, 0, v1alpha1.JobStateUnknown, nil),
						newJobStatus("azure", "release-openshift-origin-installer-e2e-azure-upgrade", 0, 0, v1alpha1.JobStateUnknown, nil),
						newJobStatus("gcp", "release-openshift-origin-installer-e2e-gcp-upgrade", 0, 0, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.22",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					UpgradeJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws", "release-openshift-origin-installer-e2e-aws-upgrade", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
							newJobRunResult("4.11.22-upgrade-from-4.10.18-aws", "ci", "build01", v1alpha1.JobRunStateSuccess, "https://abc.123.com", v1alpha1.JobRunUpgradeTypeUpgradeMinor),
						}),
						newJobStatus("azure", "release-openshift-origin-installer-e2e-azure-upgrade", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
							newJobRunResult("4.11.22-upgrade-from-4.10.44-azure", "ci", "build02", v1alpha1.JobRunStateSuccess, "https://abc.123.com", v1alpha1.JobRunUpgradeTypeUpgradeMinor),
						}),
						newJobStatus("gcp", "release-openshift-origin-installer-e2e-gcp-upgrade", 0, 0, v1alpha1.JobStateUnknown, []v1alpha1.JobRunResult{
							newJobRunResult("4.11.22-upgrade-from-4.11.10-gcp", "ci", "build03", v1alpha1.JobRunStateSuccess, "https://abc.123.com", v1alpha1.JobRunUpgradeTypeUpgrade),
						}),
					},
				},
			},
		},
		{
			name: "InvalidPayloadVerificationDataSource",
			prowjob: []runtime.Object{
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aws-serial", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", "ocp/4.11-art-latest", "build01", v1.SuccessState, "https://abc.123.com"),
			},
			input: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: "Invalid",
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws-serial", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", 0, 0, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: "Invalid",
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws-serial", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", 0, 0, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
		},
		{
			name: "MissingPayloadVerificationDataSource",
			prowjob: []runtime.Object{
				newProwJob("4.11.0-0.nightly-2022-02-09-091559-aws-serial", "ci", "4.11.0-0.nightly-2022-02-09-091559", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", "ocp/4.11-art-latest", "build01", v1.SuccessState, "https://abc.123.com"),
			},
			input: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws-serial", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", 0, 0, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws-serial", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", 0, 0, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
		},
		{
			name:    "ImageStreamPayloadVerificationDataSource",
			prowjob: []runtime.Object{},
			input: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceImageStream,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws-serial", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", 0, 0, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.11.0-0.nightly-2022-02-09-091559",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceImageStream,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("aws-serial", "periodic-ci-openshift-release-master-nightly-4.10-e2e-aws-serial", 0, 0, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
		},
		{
			name: "ProwjobSchedulingState",
			prowjob: []runtime.Object{
				newProwJob("install-analysis-all", "ci", "4.18.0-0.nightly-2024-09-23-063227", "periodic-ci-openshift-release-master-nightly-4.18-install-analysis-all", "ocp/4.18-art-latest", "build09", v1.SchedulingState, "https://abc.123.com"),
			},
			input: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.18.0-0.nightly-2024-09-23-063227",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("install-analysis-all", "periodic-ci-openshift-release-master-nightly-4.18-install-analysis-all", 2, 0, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
			expected: &v1alpha1.ReleasePayload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "4.18.0-0.nightly-2024-09-23-063227",
					Namespace: "ocp",
				},
				Spec: v1alpha1.ReleasePayloadSpec{
					PayloadCreationConfig: v1alpha1.PayloadCreationConfig{
						ProwCoordinates: v1alpha1.ProwCoordinates{
							Namespace: "ci",
						},
					},
					PayloadVerificationConfig: v1alpha1.PayloadVerificationConfig{
						PayloadVerificationDataSource: v1alpha1.PayloadVerificationDataSourceBuildFarm,
					},
				},
				Status: v1alpha1.ReleasePayloadStatus{
					BlockingJobResults: []v1alpha1.JobStatus{
						newJobStatus("install-analysis-all", "periodic-ci-openshift-release-master-nightly-4.18-install-analysis-all", 2, 0, v1alpha1.JobStateUnknown, nil),
					},
				},
			},
			expectedErr: nil,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			prowJobClient := prowfake.NewSimpleClientset(testCase.prowjob...)

			prowJobInformerFactory := prowjobinformers.NewSharedInformerFactory(prowJobClient, controllerDefaultResyncDuration)
			prowJobInformer := prowJobInformerFactory.Prow().V1().ProwJobs()

			releasePayloadClient := fake.NewSimpleClientset(testCase.input)
			releasePayloadInformerFactory := releasepayloadinformers.NewSharedInformerFactory(releasePayloadClient, controllerDefaultResyncDuration)
			releasePayloadInformer := releasePayloadInformerFactory.Release().V1alpha1().ReleasePayloads()

			c, err := NewProwJobStatusController(releasePayloadInformer, releasePayloadClient.ReleaseV1alpha1(), prowJobInformer, events.NewInMemoryRecorder("prowjob-status-controller-test"))
			if err != nil {
				t.Fatalf("Failed to create ProwJob Status Controller: %v", err)
			}

			c.cachesToSync = append(c.cachesToSync, prowJobInformer.Informer().HasSynced)
			releasePayloadInformerFactory.Start(context.Background().Done())
			prowJobInformerFactory.Start(context.Background().Done())

			if !cache.WaitForNamedCacheSync("ProwJobStatusController", context.Background().Done(), c.cachesToSync...) {
				t.Errorf("%s: error waiting for caches to sync", testCase.name)
				return
			}

			if err := c.sync(context.TODO(), fmt.Sprintf("%s/%s", testCase.input.Namespace, testCase.input.Name)); err != nil {
				if testCase.expectedErr == nil {
					t.Fatalf("%s - encountered unexpected error: %v", testCase.name, err)
				}
				if !cmp.Equal(err.Error(), testCase.expectedErr.Error()) {
					t.Errorf("%s - expected error: %v, got: %v", testCase.name, testCase.expectedErr, err)
				}
			}

			// Performing a live lookup instead of having to wait for the cache to sink (again)...
			output, _ := c.releasePayloadClient.ReleasePayloads(testCase.input.Namespace).Get(context.TODO(), testCase.input.Name, metav1.GetOptions{})
			if !cmp.Equal(output, testCase.expected, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")) {
				t.Errorf("%s: Expected %v, got %v", testCase.name, testCase.expected, output)
			}
		})
	}
}
