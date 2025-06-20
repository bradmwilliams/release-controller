package utils

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/blang/semver"
	releasecontroller "github.com/openshift/release-controller/pkg/release-controller"
)

const (
	StreamStable    = "Stable"
	StreamCandidate = "Candidate"
)

var (
	// OCP Release Processing
	stableRelease       = regexp.MustCompile(`(?P<job>[\w\d-\\.]+?)(?:-(?P<count>\d+))?$`)
	candidateRelease    = regexp.MustCompile(`^(?P<build>[\d-]+)-(?P<job>[\w\d-\\.]+?)(?:-(?P<count>\d+))?$`)
	prerelease          = regexp.MustCompile(`^(?P<stream>[\w\d]+)-(?P<architecture>\w+)?-?(?P<timestamp>\d{4}-\d{2}-\d{2}-\d{6})-(?P<job>[\w\d-\\.]+?)(?:-(?P<count>\d+))?$`)
	upgradeFrom         = regexp.MustCompile(`^(?P<build>[\w\d\\.]+)-(?P<job>upgrade-from[\w\d-\\.]+?)(?:-(?P<count>\d+))?$`)
	automaticUpgradeJob = regexp.MustCompile(`^upgrade-from-(?P<version>[\d\\.]+)-?(?P<candidate>[e|f|r]c.\d+)?-?(?P<platform>\w+)-?(?P<count>\d+)?$`)

	// OKD Release Processing
	okdStableRelease    = regexp.MustCompile(`^(?P<stream>okd-scos|okd)$`)
	okdCandidateRelease = regexp.MustCompile(`^(?P<stream>okd-scos|okd)-?(?P<timestamp>\d{4}-\d{2}-\d{2}-\d{6})(?:-(?P<extra>.*))?$`)
)

type PreReleaseDetails struct {
	// TODO: Remove...
	Build               string
	Stream              string
	Timestamp           string
	CIConfigurationName string
	Count               string
	UpgradeFrom         string
	UpgradePlatform     string
	Architecture        string
	PreRelease          string
}

type ReleaseVerificationJobDetails struct {
	X, Y, Z uint64
	*PreReleaseDetails
}

func (d ReleaseVerificationJobDetails) ToString() string {
	count := ""
	if len(d.Count) > 0 {
		count = fmt.Sprintf("-%s", d.Count)
	}
	switch d.Stream {
	case StreamStable:
		if len(d.UpgradeFrom) > 0 {
			return fmt.Sprintf("%d.%d.%d-upgrade-from-%s-%s%s", d.X, d.Y, d.Z, d.UpgradeFrom, d.CIConfigurationName, count)
		}
		return fmt.Sprintf("%d.%d.%d-%s%s", d.X, d.Y, d.Z, d.CIConfigurationName, count)
	case StreamCandidate:
		if len(d.UpgradeFrom) > 0 {
			return fmt.Sprintf("%d.%d.%d-%s-upgrade-from-%s-%s%s", d.X, d.Y, d.Z, d.Build, d.UpgradeFrom, d.CIConfigurationName, count)
		}
		return fmt.Sprintf("%d.%d.%d-%s-%s%s", d.X, d.Y, d.Z, d.Build, d.CIConfigurationName, count)
	default:
		if len(d.Architecture) > 0 {
			return fmt.Sprintf("%d.%d.%d-%s.%s-%s-%s-%s%s", d.X, d.Y, d.Z, d.Build, d.Stream, d.Architecture, d.Timestamp, d.CIConfigurationName, count)
		}
		return fmt.Sprintf("%d.%d.%d-%s.%s-%s-%s%s", d.X, d.Y, d.Z, d.Build, d.Stream, d.Timestamp, d.CIConfigurationName, count)
	}
}

func ParseReleaseVerificationJobName(name string) (*ReleaseVerificationJobDetails, error) {
	version, err := releasecontroller.SemverParseTolerant(name)
	if err != nil {
		return nil, fmt.Errorf("error: %v", err)
	}
	pr, err := parsePreRelease(version.Pre)
	if err != nil {
		return nil, fmt.Errorf("error: %v", err)
	}
	return &ReleaseVerificationJobDetails{
		X:                 version.Major,
		Y:                 version.Minor,
		Z:                 version.Patch,
		PreReleaseDetails: pr,
	}, nil
}

func parsePreRelease(prerelease []semver.PRVersion) (*PreReleaseDetails, error) {
	details := &PreReleaseDetails{}
	splitVersion(preReleaseString(prerelease), details)
	return details, nil
}

func splitVersion(version string, details *PreReleaseDetails) {
	for key, value := range parse(version) {
		switch key {
		case "stream":
			details.Stream = value
		case "timestamp":
			details.Timestamp = value
		case "job":
			details.CIConfigurationName = value
		case "count":
			details.Count = value
		case "prerelease":
			details.PreRelease = value
		case "architecture":
			details.Architecture = value
		case "upgrade_from":
			details.UpgradeFrom = value
		case "upgrade_platform":
			details.UpgradePlatform = value
		}
	}
	// Handle OCP Streams...
	if !strings.Contains(details.Stream, "okd") {
		if len(details.PreRelease) == 0 {
			details.Stream = StreamStable
		}
		if candidates.MatchString(details.PreRelease) {
			details.Stream = StreamCandidate
		}
	}
}

var (
	candidates = regexp.MustCompile(`[efr]c.\d+`)
	expressions = []*regexp.Regexp{
		regexp.MustCompile(`^(?P<prerelease>[\d-]+)\.(?P<stream>okd-scos|okd|\w+)-(?P<architecture>\w+)?-?(?P<timestamp>\d{4}-\d{2}-\d{2}-\d{6})-(?P<job>[\w-\\.]+?)(?:-(?P<count>\d+))?$`),
		regexp.MustCompile(`^(?P<prerelease>[efr]c\.\d+)?-?upgrade-from-(?P<upgrade_from>[\d\\.]+(?:-[efr]c.\d+)?)-?(?P<upgrade_platform>\w+)-?(?P<count>\d+)?$`),
		regexp.MustCompile(`^(?P<stream>okd-scos|okd)\.(?P<prerelease>(?:[erf]c\.\d+)+|\d+)-(?P<job>[\w-\\.]+?)(?:-(?P<count>\d+))?$`),
		regexp.MustCompile(`^(?P<prerelease>(?:[erf]c|okd-scos|okd)\.\d+)-(?P<job>[\w-\\.]+?)(?:-(?P<count>\d+))?$`),
		regexp.MustCompile(`^(?P<job>[\w-\\.]+?)(?:-(?P<count>\d+))?$`),
	}
)

func preReleaseString(pre []semver.PRVersion) string {
	var pieces []string
	for _, item := range pre {
		pieces = append(pieces, item.String())
	}
	return strings.Join(pieces, ".")
}

func parse(prerelease string) map[string]string {
	result := make(map[string]string)
	for _, expression := range expressions {
		if expression.MatchString(prerelease) {
			matches := expression.FindStringSubmatch(prerelease)
			for i, name := range expression.SubexpNames() {
				if i != 0 && name != "" {
					result[name] = matches[i]
				}
			}
			break
		}
	}
	return result
}

func generateCIConfigurationName(prerelease []semver.PRVersion) string {
	var pieces []string
	for idx := range prerelease {
		switch {
		case prerelease[idx].IsNum:
			pieces = append(pieces, fmt.Sprintf("%d", prerelease[idx].VersionNum))
		default:
			pieces = append(pieces, prerelease[idx].VersionStr)
		}
	}
	return strings.Join(pieces, ".")
}
