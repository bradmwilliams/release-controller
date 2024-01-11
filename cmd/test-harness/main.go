package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/blang/semver"
	v1 "github.com/openshift/api/image/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"os"
	goruntime "runtime"
	"strings"
	"time"

	releasepayloadinformers "github.com/openshift/release-controller/pkg/client/informers/externalversions"

	releasepayloadclient "github.com/openshift/release-controller/pkg/client/clientset/versioned"
	"github.com/openshift/release-controller/pkg/jira"

	releasecontroller "github.com/openshift/release-controller/pkg/release-controller"

	_ "net/http/pprof" // until openshift/library-go#309 merges

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/spf13/cobra"
	"k8s.io/klog"

	jiraBaseClient "github.com/andygrunwald/go-jira"
	imageclientset "github.com/openshift/client-go/image/clientset/versioned"
	imageinformers "github.com/openshift/client-go/image/informers/externalversions"
	"github.com/openshift/library-go/pkg/serviceability"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	prowconfig "k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/config/secret"
	"k8s.io/test-infra/prow/flagutil"
	configflagutil "k8s.io/test-infra/prow/flagutil/config"
	pluginflagutil "k8s.io/test-infra/prow/flagutil/plugins"
	"k8s.io/test-infra/prow/interrupts"
	k8sjiraBaseClient "k8s.io/test-infra/prow/jira"
)

type options struct {
	ReleaseNamespaces []string
	PublishNamespaces []string
	JobNamespace      string
	ProwNamespace     string

	ProwJobKubeconfig    string
	NonProwJobKubeconfig string
	ReleasesKubeconfig   string
	ToolsKubeconfig      string

	prowconfig configflagutil.ConfigOptions

	ListenAddr    string
	ArtifactsHost string

	AuditStorage           string
	AuditGCSServiceAccount string
	SigningKeyring         string
	CLIImageForAudit       string
	ToolsImageStreamTag    string

	DryRun       bool
	LimitSources []string

	PluginConfig pluginflagutil.PluginOptions

	githubThrottle int
	github         flagutil.GitHubOptions

	VerifyJira bool
	jira       flagutil.JiraOptions

	validateConfigs string

	softDeleteReleaseTags bool

	ReleaseArchitecture string

	AuthenticationMessage string

	Registry string

	ClusterGroups []string

	ARTSuffix string

	PruneGraph        bool
	PrintPrunedGraph  string
	ConfirmPruneGraph bool

	ProcessLegacyResults bool
	ManifestListMode     bool
}

// Add metrics for jira verifier errors
var (
	jiraErrorMetrics = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "release_controller_jira_errors_total",
			Help: "The total number of errors encountered by the release-controller's jira verifier",
		},
		[]string{"type"},
	)
)

func main() {
	serviceability.StartProfiler()
	defer serviceability.Profile(os.Getenv("OPENSHIFT_PROFILE")).Stop()
	// prow registers this on init
	interrupts.OnInterrupt(func() { os.Exit(0) })

	original := flag.CommandLine
	klog.InitFlags(original)
	original.Set("alsologtostderr", "true")
	original.Set("v", "2")

	opt := &options{
		ListenAddr:          ":8080",
		ToolsImageStreamTag: ":tests",

		Registry: "registry.ci.openshift.org",

		PrintPrunedGraph: releasecontroller.PruneGraphPrintSecret,
	}
	cmd := &cobra.Command{
		Run: func(cmd *cobra.Command, arguments []string) {
			if f := cmd.Flags().Lookup("audit"); f.Changed && len(f.Value.String()) == 0 {
				klog.Exitf("error: --audit must include a location to store audit logs")
			}
			if err := opt.Run(); err != nil {
				klog.Exitf("error: %v", err)
			}
		},
	}
	flagset := cmd.Flags()
	flagset.BoolVar(&opt.DryRun, "dry-run", opt.DryRun, "Perform no actions on the release streams")
	flagset.StringVar(&opt.AuditStorage, "audit", opt.AuditStorage, "A storage location to report audit logs to, if specified. The location may be a file://path or gs:// GCS bucket and path.")
	flagset.StringVar(&opt.AuditGCSServiceAccount, "audit-gcs-service-account", opt.AuditGCSServiceAccount, "An optional path to a service account file that should be used for uploading audit information to GCS.")
	flagset.StringSliceVar(&opt.LimitSources, "only-source", opt.LimitSources, "The names of the image streams to operate on. Intended for testing.")
	flagset.StringVar(&opt.SigningKeyring, "sign", opt.SigningKeyring, "The OpenPGP keyring to sign releases with. Only releases that can be verified will be signed.")
	flagset.StringVar(&opt.CLIImageForAudit, "audit-cli-image", opt.CLIImageForAudit, "The command line image pullspec to use for audit and signing. This should be set to a digest under the signers control to prevent attackers from forging verification. If you pass 'local' the oc binary on the path will be used instead of running a job.")

	flagset.StringVar(&opt.ToolsImageStreamTag, "tools-image-stream-tag", opt.ToolsImageStreamTag, "An image stream tag pointing to a release stream that contains the oc command and git (usually <master>:tests).")

	var ignored string
	flagset.StringVar(&ignored, "to", ignored, "REMOVED: The image stream in the release namespace to push releases to.")

	flagset.StringVar(&opt.ProwJobKubeconfig, "prow-job-kubeconfig", opt.ProwJobKubeconfig, "The kubeconfig to use for interacting with ProwJobs. Defaults in-cluster config if unset.")
	flagset.StringVar(&opt.NonProwJobKubeconfig, "non-prow-job-kubeconfig", opt.NonProwJobKubeconfig, "The kubeconfig to use for everything that is not prowjobs (namespaced, pods, batchjobs, ....). Falls back to incluster config if unset")
	flagset.StringVar(&opt.ReleasesKubeconfig, "releases-kubeconfig", opt.ReleasesKubeconfig, "The kubeconfig to use for interacting with release imagestreams and jobs. Falls back to non-prow-job-kubeconfig and then incluster config if unset")
	flagset.StringVar(&opt.ToolsKubeconfig, "tools-kubeconfig", opt.ToolsKubeconfig, "The kubeconfig to use for running the release-controller tools. Falls back to non-prow-job-kubeconfig and then incluster config if unset")

	flagset.StringVar(&opt.JobNamespace, "job-namespace", opt.JobNamespace, "The namespace to execute jobs and hold temporary objects.")
	flagset.StringSliceVar(&opt.ReleaseNamespaces, "release-namespace", opt.ReleaseNamespaces, "The namespace where the source image streams are located and where releases will be published to.")
	flagset.StringSliceVar(&opt.PublishNamespaces, "publish-namespace", opt.PublishNamespaces, "Optional namespaces that the release might publish results to.")
	flagset.StringVar(&opt.ProwNamespace, "prow-namespace", opt.ProwNamespace, "The namespace where the Prow jobs will be created (defaults to --job-namespace).")

	opt.prowconfig.ConfigPathFlagName = "prow-config"
	opt.prowconfig.JobConfigPathFlagName = "job-config"
	opt.prowconfig.AddFlags(flag.CommandLine)
	flagset.AddGoFlagSet(flag.CommandLine)

	flagset.StringVar(&opt.ArtifactsHost, "artifacts", opt.ArtifactsHost, "REMOVED: The public hostname of the artifacts server.")

	flagset.StringVar(&opt.ListenAddr, "listen", opt.ListenAddr, "The address to serve metrics on")

	flagset.BoolVar(&opt.VerifyJira, "verify-jira", opt.VerifyJira, "Update status of issues fixed in accepted release to VERIFIED if PR was approved by QE.")
	flagset.IntVar(&opt.githubThrottle, "github-throttle", 0, "Maximum number of GitHub requests per hour. Used by jira verifier.")

	flagset.StringVar(&opt.validateConfigs, "validate-configs", "", "Validate configs at specified directory and exit without running operator")
	flagset.BoolVar(&opt.softDeleteReleaseTags, "soft-delete-release-tags", false, "If set to true, annotate imagestreamtags instead of deleting them")

	flagset.StringVar(&opt.ReleaseArchitecture, "release-architecture", opt.ReleaseArchitecture, "The architecture of the releases to be created (defaults to 'amd64' if not specified).")

	flagset.StringVar(&opt.AuthenticationMessage, "authentication-message", opt.AuthenticationMessage, "HTML formatted string to display a registry authentication message")

	flagset.StringVar(&opt.Registry, "registry", opt.Registry, "Specify the registry, that the artifact server will use, to retrieve release images when located on remote clusters")

	flagset.StringVar(&opt.ARTSuffix, "art-suffix", "", "Suffix for ART imagstreams (eg. `-art-latest`)")

	// This option can be used to group, any number of, similar build cluster names into logical groups that will be used to
	// randomly distribute prowjobs onto.  When the release-controller reads in the prowjob definition, it will check if
	// the defined "cluster:" value exists in one of the cluster groups and then "Get()" the next, random, cluster name
	// from the group and assign the job to that cluster. If the cluster: is not a member of any group, then the
	// release-controller will not make any modifications and the jobs will run on the cluster as it is defined in the
	// job itself. The groupings are intended to be used to pool build clusters of similar configurations (i.e. cloud
	// provider, specific hardware, configurations, etc).  This way, jobs that are intended to be run on the specific
	// configurations can be distributed properly on the environment that they require.
	flagset.StringArrayVar(&opt.ClusterGroups, "cluster-group", opt.ClusterGroups, "A comma seperated list of build cluster names to evenly distribute jobs to.  May be specified multiple times to account for different configurations of build clusters.")

	flagset.BoolVar(&opt.PruneGraph, "prune-graph", opt.PruneGraph, "Reads the upgrade graph, prunes edges, and prints the result")
	flagset.StringVar(&opt.PrintPrunedGraph, "print-pruned-graph", opt.PrintPrunedGraph, "Print the result of pruning the graph.  Valid options are: <|secret|debug>. The default, 'secret', is the base64 encoded secret payload. The 'debug' option will pretty print the json payload")
	flagset.BoolVar(&opt.ConfirmPruneGraph, "confirm-prune-graph", opt.ConfirmPruneGraph, "Persist the pruned graph")

	flagset.BoolVar(&opt.ProcessLegacyResults, "process-legacy-results", opt.ProcessLegacyResults, "enable the migration of imagestream based results to ReleasePayloads")
	flagset.BoolVar(&opt.ManifestListMode, "manifest-list-mode", opt.ManifestListMode, "enable manifest list support for oc operations")

	goFlagSet := flag.NewFlagSet("prowflags", flag.ContinueOnError)
	opt.github.AddFlags(goFlagSet)
	opt.jira.AddFlags(goFlagSet)
	opt.PluginConfig.AddFlags(goFlagSet)
	flagset.AddGoFlagSet(goFlagSet)

	flagset.AddGoFlag(original.Lookup("v"))

	if err := cmd.Execute(); err != nil {
		klog.Exitf("error: %v", err)
	}
}

func (o *options) Run() error {
	if o.validateConfigs != "" {
		return validateConfigs(o.validateConfigs)
	}
	tagParts := strings.Split(o.ToolsImageStreamTag, ":")
	if len(tagParts) != 2 || len(tagParts[1]) == 0 {
		return fmt.Errorf("--tools-image-stream-tag must be STREAM:TAG or :TAG (default STREAM is the oldest release stream)")
	}
	if len(o.ReleaseNamespaces) == 0 {
		return fmt.Errorf("no namespace set, use --release-namespace")
	}
	if len(o.JobNamespace) == 0 {
		return fmt.Errorf("no job namespace set, use --job-namespace")
	}
	if len(o.ProwNamespace) == 0 {
		o.ProwNamespace = o.JobNamespace
	}
	if sets.NewString(o.ReleaseNamespaces...).HasAny(o.PublishNamespaces...) {
		return fmt.Errorf("--release-namespace and --publish-namespace may not overlap")
	}
	var architecture = "amd64"
	if len(o.ReleaseArchitecture) > 0 {
		architecture = o.ReleaseArchitecture
	}
	if len(o.PrintPrunedGraph) > 0 && o.PrintPrunedGraph != releasecontroller.PruneGraphPrintSecret && o.PrintPrunedGraph != releasecontroller.PruneGraphPrintDebug {
		return fmt.Errorf("--print-prune-graph must be \"%s\" or \"%s\"", releasecontroller.PruneGraphPrintSecret, releasecontroller.PruneGraphPrintDebug)
	}
	inClusterCfg, err := loadClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to load incluster config: %w", err)
	}
	config, err := o.nonProwJobKubeconfig(inClusterCfg)
	if err != nil {
		return fmt.Errorf("failed to load config from %s: %w", o.NonProwJobKubeconfig, err)
	}
	releasesConfig, err := o.releasesKubeconfig(inClusterCfg)
	if err != nil {
		return fmt.Errorf("failed to load releases config from %s: %w", o.ReleasesKubeconfig, err)
	}
	toolsConfig, err := o.toolsKubeconfig(inClusterCfg)
	if err != nil {
		return fmt.Errorf("failed to load tools config from %s: %w", o.ToolsKubeconfig, err)
	}
	var mode string
	switch {
	case o.DryRun:
		mode = "dry-run"
	case len(o.AuditStorage) > 0:
		mode = "audit"
	case len(o.LimitSources) > 0:
		mode = "manage"
	default:
		mode = "manage"
	}
	config.UserAgent = fmt.Sprintf("release-controller/%s (%s/%s) %s", version.Get().GitVersion, goruntime.GOOS, goruntime.GOARCH, mode)
	releasesConfig.UserAgent = fmt.Sprintf("release-controller/%s (%s/%s) %s", version.Get().GitVersion, goruntime.GOOS, goruntime.GOARCH, mode)
	toolsConfig.UserAgent = fmt.Sprintf("release-controller/%s (%s/%s) %s", version.Get().GitVersion, goruntime.GOOS, goruntime.GOARCH, mode)

	client, err := clientset.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("unable to create client: %v", err)
	}
	releasesClient, err := clientset.NewForConfig(releasesConfig)
	if err != nil {
		return fmt.Errorf("unable to create releases client: %v", err)
	}
	toolsClient, err := clientset.NewForConfig(toolsConfig)
	if err != nil {
		return fmt.Errorf("unable to create tools client: %v", err)
	}
	releaseNamespace := o.ReleaseNamespaces[0]
	for _, ns := range o.ReleaseNamespaces {
		if _, err := client.CoreV1().Namespaces().Get(context.TODO(), ns, metav1.GetOptions{}); err != nil {
			return fmt.Errorf("unable to find release namespace: %s: %v", ns, err)
		}
	}
	if o.JobNamespace != releaseNamespace {
		if _, err := client.CoreV1().Namespaces().Get(context.TODO(), o.JobNamespace, metav1.GetOptions{}); err != nil {
			return fmt.Errorf("unable to find job namespace: %v", err)
		}
	}
	klog.Infof("%s releases will be sourced from the following namespaces: %s, and jobs will be run in %s", strings.Title(architecture), strings.Join(o.ReleaseNamespaces, " "), o.JobNamespace)

	imageClient, err := imageclientset.NewForConfig(releasesConfig)
	if err != nil {
		return fmt.Errorf("unable to create image client: %v", err)
	}

	prowClient, err := o.prowJobClient(inClusterCfg)
	if err != nil {
		return fmt.Errorf("failed to create prowjob client: %v", err)
	}

	stopCh := wait.NeverStop
	var hasSynced []cache.InformerSynced

	batchFactory := informers.NewSharedInformerFactoryWithOptions(releasesClient, 10*time.Minute, informers.WithNamespace(o.JobNamespace))
	jobs := batchFactory.Batch().V1().Jobs()
	hasSynced = append(hasSynced, jobs.Informer().HasSynced)

	configAgent := &prowconfig.Agent{}
	if o.prowconfig.ConfigPath != "" {
		var err error
		configAgent, err = o.prowconfig.ConfigAgent()
		if err != nil {
			return err
		}
	}

	var jiraClient k8sjiraBaseClient.Client
	var tokens []string
	// Append the path of github secrets.
	if o.github.TokenPath != "" {
		tokens = append(tokens, o.github.TokenPath)
	}
	if o.github.AppPrivateKeyPath != "" {
		tokens = append(tokens, o.github.AppPrivateKeyPath)
	}
	if err := secret.Add(tokens...); err != nil {
		return fmt.Errorf("Error starting secrets agent: %w", err)
	}

	ghClient, err := o.github.GitHubClient(false)
	if err != nil {
		return fmt.Errorf("Failed to create github client: %v", err)
	}
	ghClient.Throttle(o.githubThrottle, 0)

	if o.VerifyJira {
		jiraClient, err = o.jira.Client()
		if err != nil {
			return fmt.Errorf("Failed to create jira client: %v", err)
		}
	}

	imageCache := releasecontroller.NewLatestImageCache(tagParts[0], tagParts[1])
	execReleaseInfo := releasecontroller.NewExecReleaseInfo(toolsClient, toolsConfig, o.JobNamespace, releaseNamespace, imageCache.Get, jiraClient)
	releaseInfo := releasecontroller.NewCachingReleaseInfo(execReleaseInfo, 64*1024*1024, architecture)

	graph := releasecontroller.NewUpgradeGraph(architecture)

	releasePayloadClient, err := releasepayloadclient.NewForConfig(inClusterCfg)
	if err != nil {
		klog.Fatal(err)
	}

	c := NewController(
		client.CoreV1(),
		imageClient.ImageV1(),
		releasesClient.BatchV1(),
		jobs,
		client.CoreV1(),
		configAgent,
		prowClient.Namespace(o.ProwNamespace),
		o.JobNamespace,
		releaseInfo,
		graph,
		o.softDeleteReleaseTags,
		o.AuthenticationMessage,
		o.ClusterGroups,
		architecture,
		o.ARTSuffix,
		releasePayloadClient.ReleaseV1alpha1(),
		o.ManifestListMode,
	)

	if o.VerifyJira {
		pluginAgent, err := o.PluginConfig.PluginAgent()
		if err != nil {
			return fmt.Errorf("Failed to create plugin agent: %v", err)
		}
		c.jiraVerifier = jira.NewVerifier(jiraClient, ghClient, pluginAgent.Config())
		initializeJiraMetrics(jiraErrorMetrics)
		c.jiraErrorMetrics = jiraErrorMetrics
	}

	batchFactory.Start(stopCh)

	// register the releasepayload namespaces
	for _, ns := range o.ReleaseNamespaces {
		releasePayloadInformerFactory := releasepayloadinformers.NewSharedInformerFactoryWithOptions(releasePayloadClient, 24*time.Hour, releasepayloadinformers.WithNamespace(ns))
		releasePayloadInformer := releasePayloadInformerFactory.Release().V1alpha1().ReleasePayloads()
		c.AddReleasePayloadNamespace(ns, releasePayloadInformer)
		hasSynced = append(hasSynced, releasePayloadInformer.Informer().HasSynced)
		releasePayloadInformerFactory.Start(stopCh)
	}

	// register the publish and release namespaces
	publishNamespaces := sets.NewString(o.PublishNamespaces...)
	for _, ns := range sets.NewString(o.ReleaseNamespaces...).Union(publishNamespaces).List() {
		factory := imageinformers.NewSharedInformerFactoryWithOptions(imageClient, 10*time.Minute, imageinformers.WithNamespace(ns))
		streams := factory.Image().V1().ImageStreams()
		if publishNamespaces.Has(ns) {
			c.AddPublishNamespace(ns, streams)
		} else {
			c.AddReleaseNamespace(ns, streams)
		}
		hasSynced = append(hasSynced, streams.Informer().HasSynced)
		factory.Start(stopCh)
	}
	imageCache.SetLister(c.releaseLister.ImageStreams(releaseNamespace))

	klog.Infof("Waiting for caches to sync")
	cache.WaitForCacheSync(stopCh, hasSynced...)

	klog.Infof("Managing releases")

	//c.RunSync(3, stopCh)

	//err = c.testSyncJira()
	//if err != nil {
	//	klog.Errorf("Caught error: %v", err)
	//	return err
	//}

	release, err := c.loadReleaseForSync("ocp", "4.16")
	if err != nil || release == nil {
		return err
	}

	now := time.Now()
	adoptTags, pendingTags, removeTags, hasNewImages, inputImageHash, queueAfter := calculateSyncActions(release, now)

	if klog.V(4) {
		klog.Infof("name=%s hasNewImages=%t inputImageHash=%s adoptTags=%v removeTags=%v pendingTags=%v queueAfter=%s", release.Source.Name, hasNewImages, inputImageHash, releasecontroller.TagNames(adoptTags), releasecontroller.TagNames(removeTags), releasecontroller.TagNames(pendingTags), queueAfter)
	}

	return nil
}

func (o *options) prowJobClient(cfg *rest.Config) (dynamic.NamespaceableResourceInterface, error) {
	if o.ProwJobKubeconfig != "" {
		var err error
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: o.ProwJobKubeconfig},
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load prowjob kubeconfig from path %q: %v", o.ProwJobKubeconfig, err)
		}
	}

	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("unable to create prow client: %v", err)
	}

	return dynamicClient.Resource(schema.GroupVersionResource{Group: "prow.k8s.io", Version: "v1", Resource: "prowjobs"}), nil
}

func (o *options) nonProwJobKubeconfig(inClusterCfg *rest.Config) (*rest.Config, error) {
	if o.NonProwJobKubeconfig == "" {
		return inClusterCfg, nil
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: o.NonProwJobKubeconfig},
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
}

func (o *options) releasesKubeconfig(inClusterCfg *rest.Config) (*rest.Config, error) {
	if o.ReleasesKubeconfig == "" {
		return o.nonProwJobKubeconfig(inClusterCfg)
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: o.ReleasesKubeconfig},
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
}

func (o *options) toolsKubeconfig(inClusterCfg *rest.Config) (*rest.Config, error) {
	if o.ToolsKubeconfig == "" {
		return o.nonProwJobKubeconfig(inClusterCfg)
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: o.ToolsKubeconfig},
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
}

// loadClusterConfig loads connection configuration
// for the cluster we're deploying to. We prefer to
// use in-cluster configuration if possible, but will
// fall back to using default rules otherwise.
func loadClusterConfig() (*rest.Config, error) {
	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
	clusterConfig, err := cfg.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("could not load client configuration: %v", err)
	}
	return clusterConfig, nil
}

// syncJira checks whether fixed bugs in a release had their
// PR reviewed and approved by the QA contact for the bug
func (c *Controller) testSyncJira() error {
	defer func() {
		if err := recover(); err != nil {
			panic(err)
		}
	}()

	key := queueKey{
		name:      "4.13-art-latest",
		namespace: "ocp",
	}

	release, err := c.loadReleaseForSync(key.namespace, key.name)
	if err != nil || release == nil {
		klog.V(6).Infof("jira: could not load release for sync for %s/%s", key.namespace, key.name)
		return err
	}

	klog.V(6).Infof("checking if %v (%s) has verifyIssues set", key, release.Config.Name)

	// check if verifyIssues publish step is enabled for release
	var verifyIssues *releasecontroller.PublishVerifyIssues
	for _, publishType := range release.Config.Publish {
		if publishType.Disabled {
			continue
		}
		switch {
		case publishType.VerifyIssues != nil:
			verifyIssues = publishType.VerifyIssues
		}
		if verifyIssues != nil {
			break
		}
	}
	if verifyIssues == nil {
		klog.V(6).Infof("%v (%s) does not have verifyIssues set", key, release.Config.Name)
		return nil
	}

	klog.V(4).Infof("Verifying fixed issues in %s", release.Config.Name)

	// get accepted tags
	//acceptedTags := releasecontroller.SortedRawReleaseTags(release, releasecontroller.ReleasePhaseAccepted)
	//tag, prevTag := getNonVerifiedTagsJira(acceptedTags)
	//if tag == nil {
	//	klog.V(6).Infof("jira: All accepted tags for %s have already been verified", release.Config.Name)
	//	return nil
	//}

	tag := &v1.TagReference{
		Name: "4.13.0-0.nightly-2023-10-03-014914",
	}
	prevTag := &v1.TagReference{
		Name: "4.13.0-0.nightly-2023-10-02-052111",
	}

	if prevTag == nil {
		if verifyIssues.PreviousReleaseTag == nil {
			klog.V(2).Infof("jira error: previous release unset for %s", release.Config.Name)
			c.jiraErrorMetrics.WithLabelValues(jiraPrevReleaseUnset).Inc()
			return fmt.Errorf("jira error: previous release unset for %s", release.Config.Name)
		}
		stream, err := c.imageClient.ImageStreams(verifyIssues.PreviousReleaseTag.Namespace).Get(context.TODO(), verifyIssues.PreviousReleaseTag.Name, metav1.GetOptions{})
		if err != nil {
			klog.V(2).Infof("jira: failed to get imagestream (%s/%s) when getting previous release for %s: %v", verifyIssues.PreviousReleaseTag.Namespace, verifyIssues.PreviousReleaseTag.Name, release.Config.Name, err)
			c.jiraErrorMetrics.WithLabelValues(jiraPrevImagestreamGetErr).Inc()
			return err
		}
		prevTag = releasecontroller.FindTagReference(stream, verifyIssues.PreviousReleaseTag.Tag)
		if prevTag == nil {
			klog.V(2).Infof("jira: failed to get tag %s in imagestream (%s/%s) when getting previous release for %s", verifyIssues.PreviousReleaseTag.Tag, verifyIssues.PreviousReleaseTag.Namespace, verifyIssues.PreviousReleaseTag.Name, release.Config.Name)
			c.jiraErrorMetrics.WithLabelValues(jiraMissingTag).Inc()
			return fmt.Errorf("failed to find tag %s in imagestream %s/%s", verifyIssues.PreviousReleaseTag.Tag, verifyIssues.PreviousReleaseTag.Namespace, verifyIssues.PreviousReleaseTag.Name)
		}
	}

	dockerRepo := release.Target.Status.PublicDockerImageRepository
	if len(dockerRepo) == 0 {
		klog.V(4).Infof("jira: release target %s does not have a configured registry", release.Target.Name)
		c.jiraErrorMetrics.WithLabelValues(jiraNoRegistry).Inc()
		return fmt.Errorf("jira: release target %s does not have a configured registry", release.Target.Name)
	}

	issues, err := c.releaseInfo.Bugs(dockerRepo+":"+prevTag.Name, dockerRepo+":"+tag.Name)
	var issueList []string
	for _, issue := range issues {
		if issue.Source == 1 {
			issueList = append(issueList, issue.ID)
		}
	}
	if err != nil {
		klog.V(4).Infof("Jira: Unable to generate bug list from %s to %s: %v", prevTag.Name, tag.Name, err)
		c.jiraErrorMetrics.WithLabelValues(jiraUnableToGenerateBuglist).Inc()
		return fmt.Errorf("jira: unable to generate bug list from %s to %s: %w", prevTag.Name, tag.Name, err)
	}
	var errs []error
	if errs := append(errs, c.jiraVerifier.VerifyIssues(issueList, tag.Name)...); len(errs) != 0 {
		klog.V(4).Infof("Error(s) in jira verifier: %v", utilerrors.NewAggregate(errs))
		c.jiraErrorMetrics.WithLabelValues(jiraVerifier).Inc()
		return utilerrors.NewAggregate(errs)
	}

	// Handle feature tags
	// Generate the change log from image digests; this should be pretty quick since the Bugs function was run recently
	changelogJSON, err := c.releaseInfo.ChangeLog(dockerRepo+":"+prevTag.Name, dockerRepo+":"+tag.Name, true)
	if err != nil {
		klog.V(4).Infof("Jira: Unable to generate changelog from %s to %s: %v", prevTag.Name, tag.Name, err)
		c.jiraErrorMetrics.WithLabelValues(jiraChangelogGeneration).Inc()
		return fmt.Errorf("jira: unable to generate changelog from %s to %s: %w", prevTag.Name, tag.Name, err)
	}
	var changelog releasecontroller.ChangeLog
	if err := json.Unmarshal([]byte(changelogJSON), &changelog); err != nil {
		klog.V(4).Infof("Jira: Unable to unmarshal changelog from %s to %s: %v", prevTag.Name, tag.Name, err)
		c.jiraErrorMetrics.WithLabelValues(jiraChangelogUnmarshal).Inc()
		return fmt.Errorf("jira: unable to unmarshal changelog from %s to %s: %w", prevTag.Name, tag.Name, err)
	}
	// Get issue details
	info, err := c.releaseInfo.IssuesInfo(changelogJSON)
	if err != nil {
		klog.V(4).Infof("Jira: Unable to parse issue info from changelog of %s to %s: %v", prevTag.Name, tag.Name, err)
		c.jiraErrorMetrics.WithLabelValues(jiraIssuesParse).Inc()
		return fmt.Errorf("jira: unable to parse issue info from changelog of %s to %s: %w", prevTag.Name, tag.Name, err)
	}

	var mapIssueDetails map[string]releasecontroller.IssueDetails
	if err := json.Unmarshal([]byte(info), &mapIssueDetails); err != nil {
		klog.V(4).Infof("Jira: Unable to unmarshal issue info from changelog of %s to %s: %v", prevTag.Name, tag.Name, err)
		c.jiraErrorMetrics.WithLabelValues(jiraIssuesUnmarshal).Inc()
		return fmt.Errorf("jira: unable to unmarshal issue info from changelog of %s to %s: %w", prevTag.Name, tag.Name, err)
	}

	parentFeatures := []string{}
	for key, details := range mapIssueDetails {
		if strings.HasPrefix(key, "OSDOCS-") || strings.HasPrefix(key, "PLMCORE-") || strings.HasPrefix(key, "OCPSTRAT-") {
			continue
		}
		if details.IssueType == releasecontroller.JiraTypeFeature {
			parentFeatures = append(parentFeatures, key)
		}
	}

	// this gets all children of the feature that are type `Epic`
	featureChildrenJSON, err := c.releaseInfo.GetFeatureChildren(parentFeatures, 10*time.Minute)
	if err != nil {
		klog.V(4).Infof("Jira: Error getting feature children: %v", err)
		//c.jiraErrorMetrics.WithLabelValues(jiraFeatureChildren).Inc()
		return fmt.Errorf("jira: unable to get feature children: %v", err)
	}
	var featureChildren map[string][]jiraBaseClient.Issue
	if err := json.Unmarshal([]byte(featureChildrenJSON), &featureChildren); err != nil {
		klog.V(4).Infof("Jira: Error unmarhsalling feature children: %v", err)
		//c.jiraErrorMetrics.WithLabelValues(jiraFeatureChildrenUnmarshal).Inc()
		return fmt.Errorf("jira: unable unmarshal feature children: %v", err)
	}

	fixVersionUpdateList := sets.New[string]()
	for _, issue := range issueList {
		if strings.HasPrefix(issue, "OSDOCS-") || strings.HasPrefix(issue, "PLMCORE-") || strings.HasPrefix(issue, "OCPSTRAT-") {
			continue
		}
		fixVersionUpdateList = fixVersionUpdateList.Insert(issue)
		if mapIssueDetails[issue].Epic != "" && !(strings.HasPrefix(mapIssueDetails[issue].Epic, "OSDOCS-") || strings.HasPrefix(mapIssueDetails[issue].Epic, "PLMCORE-") || strings.HasPrefix(mapIssueDetails[issue].Epic, "OCPSTRAT-")) {
			fixVersionUpdateList = fixVersionUpdateList.Insert(mapIssueDetails[issue].Epic)
		}
	}
	for _, feature := range parentFeatures {
		unsetEpic := false
		for _, epic := range featureChildren[feature] {
			if strings.HasPrefix(epic.Key, "OSDOCS-") || strings.HasPrefix(epic.Key, "PLMCORE-") || strings.HasPrefix(epic.Key, "OCPSTRAT-") ||
				fixVersionUpdateList.Has(epic.Key) || (epic.Fields != nil && epic.Fields.FixVersions != nil && len(epic.Fields.FixVersions) > 0) {
				continue
			}
			unsetEpic = true
			break
		}
		if !unsetEpic {
			fixVersionUpdateList.Insert(feature)
		}
	}

	// figure out what fix version should be applied
	tagSemver := semver.MustParse(tag.Name)
	fixVersion := fmt.Sprintf("%d.%d.0", tagSemver.Major, tagSemver.Minor)
	stableReleases, err := releasecontroller.GetStableReleases(c.parsedReleaseConfigCache, c.eventRecorder, c.releaseLister)
	if err != nil {
		klog.V(4).Infof("Jira: Error getting stable releases: %v", err)
		c.jiraErrorMetrics.WithLabelValues(jiraStableReleases).Inc()
		return fmt.Errorf("jira: unable to get stable releases: %v", err)
	}
	stableVersionTag := findStableVersionTag(stableReleases, semver.MustParse(fixVersion))
	if stableVersionTag != nil {
		if timestamp, ok := stableVersionTag.Annotations[releasecontroller.ReleaseAnnotationCreationTimestamp]; ok {
			created, err := time.Parse(time.RFC3339, timestamp)
			if err == nil {
				// if 4.y.0 was published before this tag, update fixVersion to 4.y.z
				if created.Before(changelog.To.Created) {
					fixVersion = fmt.Sprintf("%d.%d.z", tagSemver.Major, tagSemver.Minor)
				}
			}
		}
	}

	if errs := append(errs, c.jiraVerifier.SetFeatureFixedVersions(fixVersionUpdateList, tag.Name, fixVersion)...); len(errs) != 0 {
		klog.V(4).Infof("Error(s) updating versions for completed features: %v", utilerrors.NewAggregate(errs))
		c.jiraErrorMetrics.WithLabelValues(jiraFeatureVersion).Inc()
		return utilerrors.NewAggregate(errs)
	}

	var lastErr error
	err = wait.PollImmediate(15*time.Second, 1*time.Minute, func() (bool, error) {
		// Get the latest version of ImageStream before trying to update annotations
		target, err := c.imageClient.ImageStreams(release.Target.Namespace).Get(context.TODO(), release.Target.Name, metav1.GetOptions{})
		if err != nil {
			klog.V(4).Infof("Failed to get latest version of target release stream %s: %v", release.Target.Name, err)
			c.jiraErrorMetrics.WithLabelValues(jiraImagestreamGetErr).Inc()
			return false, err
		}
		tagToBeUpdated := releasecontroller.FindTagReference(target, tag.Name)
		if tagToBeUpdated == nil {
			klog.V(6).Infof("release %s no longer exists, cannot set annotation %s=true", tag.Name, releasecontroller.ReleaseAnnotationIssuesVerified)
			return false, fmt.Errorf("release %s no longer exists, cannot set annotation %s=true", tag.Name, releasecontroller.ReleaseAnnotationIssuesVerified)
		}
		if tagToBeUpdated.Annotations == nil {
			tagToBeUpdated.Annotations = make(map[string]string)
		}
		tagToBeUpdated.Annotations[releasecontroller.ReleaseAnnotationIssuesVerified] = "true"
		klog.V(6).Infof("Setting %s annotation to \"true\" for %s in imagestream %s/%s", releasecontroller.ReleaseAnnotationIssuesVerified, tag.Name, target.GetNamespace(), target.GetName())
		if _, err := c.imageClient.ImageStreams(target.Namespace).Update(context.TODO(), target, metav1.UpdateOptions{}); err != nil {
			klog.V(4).Infof("Failed to update Jira annotation for tag %s in imagestream %s/%s: %v", tag.Name, target.GetNamespace(), target.GetName(), err)
			lastErr = err
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		if lastErr != nil && errors.Is(err, wait.ErrWaitTimeout) {
			err = lastErr
		}
		c.jiraErrorMetrics.WithLabelValues(jiraFailedAnnotation).Inc()
		return err
	}

	return nil
}
