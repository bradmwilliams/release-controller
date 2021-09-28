package main

import (
	"fmt"
	imagev1 "github.com/openshift/api/image/v1"
	"k8s.io/klog"
	prowjobv1 "k8s.io/test-infra/prow/apis/prowjobs/v1"
	"time"
)

const (
	// maximumAnalysisJobCount the maximum number of analysis jobs that can be executed at one time
	maximumAnalysisJobCount = 20

	// defaultAggregateProwJobName the default ProwJob to call if no override is specified
	defaultAggregateProwJobName = "release-openshift-release-analysis-aggregator"
)

// launchAnalysisJobs creates and instantiates the specified number of occurrences of the release analysis jobs for
// a given release.  The jobs can be tracked via its labels and/or annotations:
// Labels:
//     "release.openshift.io/analysis": the name of the release tag (i.e. 4.9.0-0.nightly-2021-07-12-202251)
// Annotations:
//     "release.openshift.io/image": the SHA value of the release
//     "release.openshift.io/dockerImageReference": the pull spec of the release
func (c *Controller) launchAnalysisJobs(release *Release, verifyName string, verifyType ReleaseVerification, releaseTag *imagev1.TagReference, previousTag, previousReleasePullSpec string, statusTag *imagev1.TagEvent) error {
	if verifyType.Disabled {
		klog.V(2).Infof("%s: Release analysis step %s is disabled, ignoring", release.Config.MirrorPrefix, verifyName)
		return nil
	}
	if verifyType.AggregatedProwJob.AnalysisJobCount <= 0 {
		klog.Warningf("%s: Release analysis step %s configured without analysisJobCount, ignoring", release.Config.MirrorPrefix, verifyName)
		return nil
	}
	if verifyType.AggregatedProwJob.AnalysisJobCount > maximumAnalysisJobCount {
		klog.Warningf("%s: Release analysis step %s analysisJobCount (%d) exceeds the maximum number of jobs (%d), ignoring ", release.Config.MirrorPrefix, verifyName, verifyType.AggregatedProwJob.AnalysisJobCount, maximumAnalysisJobCount)
		return nil
	}
	jobLabels := map[string]string{
		"release.openshift.io/analysis": releaseTag.Name,
	}
	jobAnnotations := map[string]string{
		"release.openshift.io/image":                statusTag.Image,
		"release.openshift.io/dockerImageReference": statusTag.DockerImageReference,
	}

	// Update the AnalysisJobCount to not trigger the analysis logic again
	copied := verifyType.DeepCopy()
	copied.AggregatedProwJob.AnalysisJobCount = 0

	for i := 0; i < verifyType.AggregatedProwJob.AnalysisJobCount; i++ {
		// Postfix the name to differentiate it from the aggregator job
		jobName := fmt.Sprintf("%s-analysis-%d", verifyName, i)
		_, err := c.ensureProwJobForReleaseTag(release, jobName, *copied, releaseTag, previousTag, previousReleasePullSpec, jobLabels, jobAnnotations)
		if err != nil {
			return err
		}
	}
	return nil
}

func addAnalysisEnvToProwJobSpec(spec *prowjobv1.ProwJobSpec, payloadTag, verificationJobName string) (bool, error) {
	if spec.PodSpec == nil {
		// Jenkins jobs cannot be parameterized
		return true, nil
	}
	for i := range spec.PodSpec.Containers {
		c := &spec.PodSpec.Containers[i]
		for j := range c.Env {
			switch name := c.Env[j].Name; {
			case name == "PAYLOAD_TAG":
				c.Env[j].Value = payloadTag
			case name == "VERIFICATION_JOB_NAME":
				c.Env[j].Value = verificationJobName
			case name == "JOB_START_TIME":
				c.Env[j].Value = time.Now().Format(time.RFC3339)
			}
		}
	}
	return true, nil
}
