package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/blang/semver"
	imagev1 "github.com/openshift/api/image/v1"
	citools "github.com/openshift/ci-tools/pkg/release/official"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog"
)

func (c *Controller) ensureVerificationJobs(release *Release, releaseTag *imagev1.TagReference) (VerificationStatusMap, error) {
	var verifyStatus VerificationStatusMap
	retryQueueDelay := 0 * time.Second
	for name, verifyType := range release.Config.Verify {
		if verifyType.Disabled {
			klog.V(2).Infof("Release verification step %s is disabled, ignoring", name)
			continue
		}

		switch {
		case verifyType.ProwJob != nil:
			if verifyStatus == nil {
				if data := releaseTag.Annotations[releaseAnnotationVerify]; len(data) > 0 {
					verifyStatus = make(VerificationStatusMap)
					if err := json.Unmarshal([]byte(data), &verifyStatus); err != nil {
						klog.Errorf("Release %s has invalid verification status, ignoring: %v", releaseTag.Name, err)
					}
				}
			}

			var jobRetries int
			if status, ok := verifyStatus[name]; ok {
				jobRetries = status.Retries
				switch status.State {
				case releaseVerificationStateSucceeded:
					continue
				case releaseVerificationStateFailed:
					jobRetries++
					if jobRetries > verifyType.MaxRetries {
						continue
					}
					// find the next time, if ok run.
					if status.TransitionTime != nil {
						backoffDuration := calculateBackoff(jobRetries-1, status.TransitionTime, &metav1.Time{Time: time.Now()})
						if backoffDuration > 0 {
							klog.V(6).Infof("%s: Release verification step %s failed %d times, last failure: %s, backoff till: %s",
								releaseTag.Name, name, jobRetries, status.TransitionTime.Format(time.RFC3339), time.Now().Add(backoffDuration).Format(time.RFC3339))
							if retryQueueDelay == 0 || backoffDuration < retryQueueDelay {
								retryQueueDelay = backoffDuration
							}
							continue
						}
					}
				case releaseVerificationStatePending:
					// we need to process this
				default:
					klog.V(2).Infof("Unrecognized verification status %q for type %s on release %s", status.State, name, releaseTag.Name)
				}
			}

			// if this is an upgrade job, find the appropriate source for the upgrade job
			var previousTag, previousReleasePullSpec string
			if verifyType.Upgrade {
				var err error
				previousTag, previousReleasePullSpec, err = c.getUpgradeTagAndPullSpec(release, releaseTag, name, verifyType.UpgradeFrom, verifyType.UpgradeFromRelease, false)
				if err != nil {
					return nil, err
				}
			}
			jobName := name
			if jobRetries > 0 {
				jobName = fmt.Sprintf("%s-%d", jobName, jobRetries)
			}

			job, err := c.ensureProwJobForReleaseTag(release, jobName, verifyType, releaseTag, previousTag, previousReleasePullSpec)
			if err != nil {
				return nil, err
			}
			status, ok := prowJobVerificationStatus(job)
			if !ok {
				return nil, fmt.Errorf("unexpected error accessing prow job definition")
			}
			if status.State == releaseVerificationStateSucceeded {
				klog.V(2).Infof("Prow job %s for release %s succeeded, logs at %s", name, releaseTag.Name, status.URL)
			}
			if verifyStatus == nil {
				verifyStatus = make(VerificationStatusMap)
			}
			status.Retries = jobRetries
			verifyStatus[name] = status

			if jobRetries >= verifyType.MaxRetries {
				verifyStatus[name].TransitionTime = nil
				continue
			}

			if status.State == releaseVerificationStateFailed {
				// Queue for retry if at least one retryable job at earliest interval
				backoffDuration := calculateBackoff(jobRetries, status.TransitionTime, &metav1.Time{Time: time.Now()})
				if retryQueueDelay == 0 || backoffDuration < retryQueueDelay {
					retryQueueDelay = backoffDuration
				}
			}

		default:
			// manual verification
		}
	}
	if retryQueueDelay > 0 {
		key := queueKey{
			name:      release.Source.Name,
			namespace: release.Source.Namespace,
		}
		c.queue.AddAfter(key, retryQueueDelay)
	}
	return verifyStatus, nil
}

func (c *Controller) getUpgradeTagAndPullSpec(release *Release, releaseTag *imagev1.TagReference, name, upgradeFrom string, upgradeFromRelease *UpgradeRelease, periodic bool) (previousTag, previousReleasePullSpec string, err error) {
	if upgradeFromRelease != nil {
		return c.resolveUpgradeRelease(upgradeFromRelease, release)
	}
	var upgradeType string
	if periodic {
		upgradeType = releaseUpgradeFromPreviousMinus1
	} else {
		upgradeType = releaseUpgradeFromPrevious
	}
	if release.Config.As == releaseConfigModeStable {
		upgradeType = releaseUpgradeFromPreviousPatch
	}
	if len(upgradeFrom) > 0 {
		upgradeType = upgradeFrom
	}
	switch upgradeType {
	case releaseUpgradeFromPrevious:
		if tags := sortedReleaseTags(release, releasePhaseAccepted); len(tags) > 0 {
			previousTag = tags[0].Name
			previousReleasePullSpec = release.Target.Status.PublicDockerImageRepository + ":" + previousTag
		}
	case releaseUpgradeFromPreviousMinus1:
		if tags := sortedReleaseTags(release, releasePhaseAccepted); len(tags) > 1 {
			previousTag = tags[1].Name
			previousReleasePullSpec = release.Target.Status.PublicDockerImageRepository + ":" + previousTag
		}
	case releaseUpgradeFromPreviousMinor:
		if version, err := semver.Parse(releaseTag.Name); err == nil && version.Minor > 0 {
			version.Minor--
			if ref, err := c.stableReleases(); err == nil {
				for _, stable := range ref.Releases {
					versions := unsortedSemanticReleaseTags(stable.Release, releasePhaseAccepted)
					sort.Sort(versions)
					if v := firstTagWithMajorMinorSemanticVersion(versions, version); v != nil {
						previousTag = v.Tag.Name
						previousReleasePullSpec = stable.Release.Target.Status.PublicDockerImageRepository + ":" + previousTag
						break
					}
				}
			}
		}
	case releaseUpgradeFromPreviousPatch:
		if version, err := semver.Parse(releaseTag.Name); err == nil {
			if ref, err := c.stableReleases(); err == nil {
				for _, stable := range ref.Releases {
					versions := unsortedSemanticReleaseTags(stable.Release, releasePhaseAccepted)
					sort.Sort(versions)
					if v := firstTagWithMajorMinorSemanticVersion(versions, version); v != nil {
						previousTag = v.Tag.Name
						previousReleasePullSpec = stable.Release.Target.Status.PublicDockerImageRepository + ":" + previousTag
						break
					}
				}
			}
		}
	default:
		return "", "", fmt.Errorf("release %s has job %s which defines invalid upgradeFrom: %s", release.Config.Name, name, upgradeType)
	}
	return previousTag, previousReleasePullSpec, err
}

func (c *Controller) resolveUpgradeRelease(upgradeRelease *UpgradeRelease, release *Release) (string, string, error) {
	if upgradeRelease.Prerelease != nil {
		semverRange, err := semver.ParseRange(upgradeRelease.Prerelease.VersionBounds.Query())
		if err != nil {
			return "", "", fmt.Errorf("invalid semver range `%s`: %w", upgradeRelease.Prerelease.VersionBounds.Query(), err)
		}
		r, latest, err := c.latestForStream("4-stable", semverRange, 0)
		if err != nil {
			return "", "", fmt.Errorf("failed to get latest tag in 4-stable stream: %w", err)
		}
		tag := latest.Name
		pullSpec := r.Target.Status.PublicDockerImageRepository + ":" + tag
		return tag, pullSpec, nil
	} else if upgradeRelease.Candidate != nil {
		// create blank semver.Range
		var constraint semver.Range
		stream := fmt.Sprintf("%s.0-0.%s%s", upgradeRelease.Candidate.Version, upgradeRelease.Candidate.Stream, strings.TrimPrefix(release.Config.To, "release"))
		r, latest, err := c.latestForStream(stream, constraint, upgradeRelease.Candidate.Relative)
		if err != nil {
			return "", "", fmt.Errorf("failed to get latest tag for stream %s: %w", stream, err)
		}
		tag := latest.Name
		pullSpec := r.Target.Status.PublicDockerImageRepository + ":" + tag
		return tag, pullSpec, nil
	} else if upgradeRelease.Official != nil {
		pullspec, version, err := citools.ResolvePullSpecAndVersion(*upgradeRelease.Official)
		if err != nil {
			return "", "", fmt.Errorf("failed to resolve official release: %w", err)
		}
		return pullspec, version, nil
	}
	return "", "", fmt.Errorf("upgradeRelease fields must be set if upgradeRelease is set")
}

func (c *Controller) ensureCandidateTests(release *Release, releaseTag *imagev1.TagReference) (map[string]*ReleaseCandidateTest, VerificationStatusList, error) {
	var testStatus VerificationStatusList
	retryQueueDelay := 0 * time.Second

	if len(releaseTag.Annotations) > 0 {
		if data := releaseTag.Annotations[releaseAnnotationCandidateTests]; len(data) > 0 {
			testStatus = make(VerificationStatusList)
			if err := json.Unmarshal([]byte(data), &testStatus); err != nil {
				glog.Errorf("Release %s has invalid verification status, ignoring: %v", releaseTag.Name, err)
			}
		}
	}

	for name, candidateTest := range release.Config.CandidateTests {
		if candidateTest.Disabled {
			glog.V(2).Infof("Release verification step %s is disabled, ignoring", name)
			continue
		}
		switch {
		case candidateTest.ProwJob != nil:
			if testStatus == nil {
				if data := releaseTag.Annotations[releaseAnnotationVerify]; len(data) > 0 {
					testStatus = make(VerificationStatusList)
					if err := json.Unmarshal([]byte(data), &testStatus); err != nil {
						glog.Errorf("Release %s has invalid verification status, ignoring: %v", releaseTag.Name, err)
					}
				}
			}

			var jobRetries int
			if status, ok := testStatus[name]; ok {
				var lastJobFailed bool
				for _, state := range status.Status {
					switch state.State {
					case releaseVerificationStateSucceeded:
						if candidateTest.RetryStrategy == RetryStrategyFirstSuccess {
							jobRetries = candidateTest.MaxRetries + 1
							break
						}
						jobRetries++
						lastJobFailed = false
						continue
					case releaseVerificationStateFailed:
						jobRetries++
						lastJobFailed = true
						if jobRetries > candidateTest.MaxRetries {
							continue
						}
					case releaseVerificationStatePending:
						lastJobFailed = false
						// we need to process this
					default:
						glog.V(2).Infof("Unrecognized verification status %q for type %s on release %s", state.State, name, releaseTag.Name)
					}
					break
				}
				// find the next time, if ok run.
				if lastJobFailed && status.TransitionTime != nil {
					backoffDuration := calculateBackoff(jobRetries-1, status.TransitionTime, &metav1.Time{time.Now()})
					if backoffDuration > 0 {
						glog.V(6).Infof("%s: Release verification step %s failed %d times, last failure: %s, backoff till: %s",
							releaseTag.Name, name, jobRetries, status.TransitionTime.Format(time.RFC3339), time.Now().Add(backoffDuration).Format(time.RFC3339))
						if retryQueueDelay == 0 || backoffDuration < retryQueueDelay {
							retryQueueDelay = backoffDuration
						}
						continue
					}
				}
			}
			if jobRetries > candidateTest.MaxRetries {
				continue
			}
			// if this is an upgrade job, find the appropriate source for the upgrade job
			var previousTag, previousReleasePullSpec string
			if candidateTest.Upgrade {
				upgradeType := releaseUpgradeFromPrevious
				if release.Config.As == releaseConfigModeStable {
					upgradeType = releaseUpgradeFromPreviousPatch
				}
				if len(candidateTest.UpgradeFrom) > 0 {
					upgradeType = candidateTest.UpgradeFrom
				}
				switch upgradeType {
				case releaseUpgradeFromPrevious:
					if tags := sortedReleaseTags(release, releasePhaseAccepted); len(tags) > 0 {
						previousTag = tags[0].Name
						previousReleasePullSpec = release.Target.Status.PublicDockerImageRepository + ":" + previousTag
					}
				case releaseUpgradeFromPreviousMinor:
					if version, err := semver.Parse(releaseTag.Name); err == nil && version.Minor > 0 {
						version.Minor--
						if ref, err := c.stableReleases(); err == nil {
							for _, stable := range ref.Releases {
								versions := unsortedSemanticReleaseTags(stable.Release, releasePhaseAccepted)
								sort.Sort(versions)
								if v := firstTagWithMajorMinorSemanticVersion(versions, version); v != nil {
									previousTag = v.Tag.Name
									previousReleasePullSpec = stable.Release.Target.Status.PublicDockerImageRepository + ":" + previousTag
									break
								}
							}
						}
					}
				case releaseUpgradeFromPreviousPatch:
					if version, err := semver.Parse(releaseTag.Name); err == nil {
						if ref, err := c.stableReleases(); err == nil {
							for _, stable := range ref.Releases {
								versions := unsortedSemanticReleaseTags(stable.Release, releasePhaseAccepted)
								sort.Sort(versions)
								if v := firstTagWithMajorMinorSemanticVersion(versions, version); v != nil {
									previousTag = v.Tag.Name
									previousReleasePullSpec = stable.Release.Target.Status.PublicDockerImageRepository + ":" + previousTag
									break
								}
							}
						}
					}
				default:
					return nil, nil, fmt.Errorf("release %s has verify type %s which defines invalid upgradeFrom: %s", release.Config.Name, name, upgradeType)
				}
			}
			jobName := name
			if jobRetries > 0 {
				jobName = fmt.Sprintf("%s-%d", jobName, jobRetries)
			}

			job, err := c.ensureProwJobForReleaseTag(release, jobName, candidateTest.ReleaseVerification, releaseTag, previousTag, previousReleasePullSpec)
			if err != nil {
				return nil, nil, err
			}
			status, ok := prowJobVerificationStatus(job)
			if !ok {
				return nil, nil, fmt.Errorf("unexpected error accessing prow job definition")
			}
			if status.State == releaseVerificationStateSucceeded {
				glog.V(2).Infof("Prow job %s for release %s succeeded, logs at %s", name, releaseTag.Name, status.URL)
				status.TransitionTime = nil
			}

			if testStatus == nil {
				testStatus = VerificationStatusList{name: &CandidateTestStatus{Status: make([]*VerifyJobStatus, 0)}}
			} else if testStatus[name] == nil {
				testStatus[name] = &CandidateTestStatus{Status: make([]*VerifyJobStatus, 0)}
			} else if testStatus[name].Status == nil {
				testStatus[name].Status = make([]*VerifyJobStatus, 0)
			}

			if jobRetries < len(testStatus[name].Status) {
				testStatus[name].Status[jobRetries] = &VerifyJobStatus{State: status.State, URL: status.URL}
			} else {
				testStatus[name].Status = append(testStatus[name].Status, &VerifyJobStatus{State: status.State, URL: status.URL})
			}
			testStatus[name].TransitionTime = status.TransitionTime
			if jobRetries >= candidateTest.MaxRetries {
				testStatus[name].TransitionTime = nil
				continue
			}

			status.Retries = jobRetries
			if jobRetries < len(testStatus[name].Status) {
				testStatus[name].Status[jobRetries] = &VerifyJobStatus{State: status.State, URL: status.URL}
			} else {
				testStatus[name].Status = append(testStatus[name].Status, &VerifyJobStatus{State: status.State, URL: status.URL})
			}
			testStatus[name].TransitionTime = status.TransitionTime

			if jobRetries >= candidateTest.MaxRetries {
				testStatus[name].TransitionTime = nil
				continue
			}

			if status.State == releaseVerificationStateFailed {
				// Queue for retry if at least one retryable job at earliest interval
				backoffDuration := calculateBackoff(jobRetries, status.TransitionTime, &metav1.Time{time.Now()})
				if retryQueueDelay == 0 || backoffDuration < retryQueueDelay {
					retryQueueDelay = backoffDuration
				}
			}

		default:
			// manual verification
		}
	}
	if retryQueueDelay > 0 {
		key := queueKey{
			name:      release.Source.Name,
			namespace: release.Source.Namespace,
		}
		c.queue.AddAfter(key, retryQueueDelay)
	}
	return release.Config.CandidateTests, testStatus, nil
}

