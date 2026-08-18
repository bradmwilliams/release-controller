package prow

import (
	"regexp"
	"testing"
)

func TestGenerateSafeProwJobName(t *testing.T) {
	testCases := []struct {
		name     string
		jobName  string
		suffix   string
		expected string
	}{
		{
			name:     "JobNameWithoutSuffixWithNoTruncation",
			jobName:  "4.9.0-0.ci-2021-08-30-130010-job-name-fake",
			suffix:   "",
			expected: "4.9.0-0.ci-2021-08-30-130010-job-name-fake",
		},
		{
			name:     "MaxSizeJobNameWithoutSuffixWithNoTruncation",
			jobName:  "4.9.0-0.ci-2021-08-30-130010-this-is-a-really-long-job-name-foo",
			suffix:   "",
			expected: "4.9.0-0.ci-2021-08-30-130010-this-is-a-really-long-job-name-foo",
		},
		{
			name:     "JobNameWithoutSuffixWithTruncation",
			jobName:  "4.9.0-0.ci-2021-08-30-130010-this-is-a-really-long-job-name-fake",
			suffix:   "",
			expected: "4.9.0-0.ci-2021-08-30-130010-this-is-a-really-long-job-fwm2xib",
		},
		{
			name:     "JobNameWithSuffixWithNoTruncation",
			jobName:  "4.9.0-0.ci-2021-08-30-130010-job-name",
			suffix:   "analysis-1",
			expected: "4.9.0-0.ci-2021-08-30-130010-job-name-analysis-1",
		},
		{
			name:     "JobNameWithSuffixWithTruncation",
			jobName:  "4.9.0-0.ci-2021-08-30-133010-this-is-a-really-long-job-name",
			suffix:   "analysis-1",
			expected: "4.9.0-0.ci-2021-08-30-133010-this-is-a-reall-18k93xt-analysis-1",
		},
		{
			name:     "MaxSizeJobNameWithSuffixWithNoTruncation",
			jobName:  "4.9.0-0.ci-2021-08-30-133010-fake-job-name-for-test1",
			suffix:   "analysis-1",
			expected: "4.9.0-0.ci-2021-08-30-133010-fake-job-name-for-test1-analysis-1",
		},
		{
			name:     "ExtremelyLongJobNameWithSuffixWithTruncation",
			jobName:  "4.9.0-0.ci-2021-08-30-133010-this-is-a-really-really-really-really-really-really-long-job-name",
			suffix:   "analysis-1",
			expected: "4.9.0-0.ci-2021-08-30-133010-this-is-a-reall-gmlwrnb-analysis-1",
		},
		{
			name:     "AggregatorJob",
			jobName:  "4.16.0-0.nightly-2024-02-07-125310-aggregated-hypershift-ovn-conformance-4.16",
			suffix:   "aggregator",
			expected: "4.16.0-0.nightly-2024-02-07-125310-aggregate-44j0w6k-aggregator",
		},
		{
			name:     "AggregatorJobWithRetry",
			jobName:  "4.16.0-0.nightly-2024-02-07-125310-aggregated-hypershift-ovn-conformance-4.16",
			suffix:   "aggregator-2",
			expected: "4.16.0-0.nightly-2024-02-07-125310-aggrega-44j0w6k-aggregator-2",
		},
		{
			name:     "TruncationResultsInInvalidJobName",
			jobName:  "4.19.0-0.nightly-2025-06-19-224840-aws-ovn-upgrade-4.19-micro-fips",
			suffix:   "1",
			expected: "4.19.0-0.nightly-2025-06-19-224840-aws-ovn-upgrade-4-mzdcpq2-1",
		},

		{
			name:     "TruncationWithAllDigitHash",
			jobName:  "4.19.0-0.nightly-2026-08-14-112411-hypershift-aks-conformance-4.19",
			suffix:   "",
			expected: "4.19.0-0.nightly-2026-08-14-112411-hypershift-aks-confo-8298822",
		},
		{
			// Exactly MaxProwJobNameLength characters, no suffix: length check is
			// strictly greater-than, so this must NOT be truncated.
			name:     "ExactlyAtMaxLengthNoTruncation",
			jobName:  "4.9.0-0.ci-2021-08-30-130010-job-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
			suffix:   "",
			expected: "4.9.0-0.ci-2021-08-30-130010-job-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		},
		{
			// One character over MaxProwJobNameLength: must be truncated.
			name:     "OneOverMaxLengthTruncates",
			jobName:  "4.9.0-0.ci-2021-08-30-130010-job-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
			suffix:   "",
			expected: "4.9.0-0.ci-2021-08-30-130010-job-xxxxxxxxxxxxxxxxxxxxxx-wnl58dt",
		},
		{
			// name + "-" + suffix lands exactly at MaxProwJobNameLength: must NOT be truncated.
			name:     "ExactlyAtMaxLengthWithSuffixNoTruncation",
			jobName:  "4.9.0-0.ci-2021-08-30-130010-job-name",
			suffix:   "yyyyyyyyyyyyyyyyyyyyyyyyy",
			expected: "4.9.0-0.ci-2021-08-30-130010-job-name-yyyyyyyyyyyyyyyyyyyyyyyyy",
		},
		{
			// name + "-" + suffix is one character over: must be truncated, and the
			// suffix itself is always preserved in full, only the name is shortened.
			name:     "OneOverMaxLengthWithSuffixTruncates",
			jobName:  "4.9.0-0.ci-2021-08-30-130010-job-name",
			suffix:   "yyyyyyyyyyyyyyyyyyyyyyyyyy",
			expected: "4.9.0-0.ci-2021-08-30-130010-8c8xsrt-yyyyyyyyyyyyyyyyyyyyyyyyyy",
		},
		{
			// Callers may pass a suffix that already has a leading "-"; it must not
			// be doubled up.
			name:     "SuffixWithLeadingDashIsNotDoubled",
			jobName:  "4.9.0-0.ci-2021-08-30-130010-job-name",
			suffix:   "-analysis-1",
			expected: "4.9.0-0.ci-2021-08-30-130010-job-name-analysis-1",
		},
	}

	t.Parallel()
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := GenerateSafeProwJobName(tc.jobName, tc.suffix)
			if result != tc.expected {
				t.Errorf("Expected truncated string %q, got %q", tc.expected, result)
			}
			if len(result) > MaxProwJobNameLength {
				t.Errorf("Expected string of length less than %d, got string of length %d", MaxProwJobNameLength, len(result))
			}
		})
	}
}

func TestProwjobSafeHash(t *testing.T) {
	testCases := []struct {
		name     string
		values   []string
		expected string
	}{
		{
			name:     "AllDigitHashCollision",
			values:   []string{"4.19.0-0.nightly-2026-08-14-112411-hypershift-aks-conformance-4.19"},
			expected: "8298822",
		},
		{
			name:     "NoValues",
			values:   []string{},
			expected: "6r2pktt",
		},
		{
			// Multiple values are hashed as a plain concatenation with no separator
			// written between them, so {"foo", "bar"} and {"foobar"} collide.
			name:     "MultipleValuesConcatenatedWithoutSeparator",
			values:   []string{"foo", "bar"},
			expected: "2rz296k",
		},
		{
			name:     "SingleConcatenatedValueMatchesSplitValues",
			values:   []string{"foobar"},
			expected: "2rz296k",
		},
	}

	t.Parallel()
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := ProwjobSafeHash(tc.values...)
			if result != tc.expected {
				t.Errorf("Expected truncated string %q, got %q", tc.expected, result)
			}
			if len(result) > MaxProwJobNameLength {
				t.Errorf("Expected string of length less than %d, got string of length %d", MaxProwJobNameLength, len(result))
			}
		})
	}
}

func TestProwjobSafeHash_Deterministic(t *testing.T) {
	values := []string{"4.19.0-0.nightly-2026-08-14-112411-hypershift-aks-conformance-4.19"}
	first := ProwjobSafeHash(values...)
	second := ProwjobSafeHash(values...)
	if first != second {
		t.Errorf("expected ProwjobSafeHash to be deterministic, got %q then %q", first, second)
	}
}

func TestProwjobSafeHash_LengthAndCharset(t *testing.T) {
	// oneWayNameEncoding's alphabet, mirrored here so a change to the charset in
	// utils.go is caught by this test rather than silently changing what job
	// names look like.
	allowedCharset := regexp.MustCompile(`^[bcdfghijklmnpqrstvwxyz0-9]{7}$`)

	inputs := [][]string{
		{},
		{"a"},
		{"a-really-long-job-name-that-needs-truncating-eventually"},
		{"multiple", "distinct", "values"},
	}
	for _, values := range inputs {
		result := ProwjobSafeHash(values...)
		if !allowedCharset.MatchString(result) {
			t.Errorf("ProwjobSafeHash(%v) = %q, want a 7-character string matching %s", values, result, allowedCharset)
		}
	}
}
