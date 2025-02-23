// Copyright Contributors to the Open Cluster Management project
package agent

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path"
	"time"

	"github.com/spf13/pflag"
	crdv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/openshift/library-go/pkg/assets"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"

	workclientset "open-cluster-management.io/api/client/work/clientset/versioned"
	workinformers "open-cluster-management.io/api/client/work/informers/externalversions"
	"open-cluster-management.io/api/feature"
	"open-cluster-management.io/registration/pkg/clientcert"
	"open-cluster-management.io/registration/pkg/spoke"
	"open-cluster-management.io/registration/pkg/spoke/managedcluster"
	"open-cluster-management.io/work/pkg/features"
	"open-cluster-management.io/work/pkg/helper"
	"open-cluster-management.io/work/pkg/spoke/auth"
	"open-cluster-management.io/work/pkg/spoke/controllers/appliedmanifestcontroller"
	"open-cluster-management.io/work/pkg/spoke/controllers/finalizercontroller"
	"open-cluster-management.io/work/pkg/spoke/controllers/manifestcontroller"
	"open-cluster-management.io/work/pkg/spoke/controllers/statuscontroller"
)

//go:embed crds
var crds embed.FS

var crdStaticFiles = []string{
	"crds/0000_01_work.open-cluster-management.io_appliedmanifestworks.crd.yaml",
	"crds/0000_02_clusters.open-cluster-management.io_clusterclaims.crd.yaml",
}

var (
	genericScheme = runtime.NewScheme()
	genericCodecs = serializer.NewCodecFactory(genericScheme)
	genericCodec  = genericCodecs.UniversalDeserializer()
)

func init() {
	utilruntime.Must(crdv1.AddToScheme(genericScheme))
}

type AgentOptions struct {
	RegistrationAgent *spoke.SpokeAgentOptions
	KubeConfig        *rest.Config
	eventRecorder     events.Recorder

	StatusSyncInterval                     time.Duration
	AppliedManifestWorkEvictionGracePeriod time.Duration

	Burst int
	QPS   float32
}

func NewAgentOptions() *AgentOptions {
	return &AgentOptions{
		RegistrationAgent:                      spoke.NewSpokeAgentOptions(),
		eventRecorder:                          events.NewInMemoryRecorder("managed-cluster-agents"),
		Burst:                                  100,
		QPS:                                    50,
		StatusSyncInterval:                     10 * time.Second,
		AppliedManifestWorkEvictionGracePeriod: 10 * time.Minute,
	}
}

func (o *AgentOptions) AddFlags(fs *pflag.FlagSet) {
	o.RegistrationAgent.AddFlags(fs)
	fs.Float32Var(&o.QPS, "spoke-kube-api-qps", o.QPS, "QPS to use while talking with apiserver on spoke cluster.")
	fs.IntVar(&o.Burst, "spoke-kube-api-burst", o.Burst, "Burst to use while talking with apiserver on spoke cluster.")
	fs.DurationVar(&o.StatusSyncInterval, "status-sync-interval", o.StatusSyncInterval, "Interval to sync resource status to hub.")
	fs.DurationVar(&o.AppliedManifestWorkEvictionGracePeriod, "appliedmanifestwork-eviction-grace-period", o.AppliedManifestWorkEvictionGracePeriod, "Grace period for appliedmanifestwork eviction")
}

func (o *AgentOptions) WithClusterName(clusterName string) *AgentOptions {
	o.RegistrationAgent.ClusterName = clusterName
	return o
}

func (o *AgentOptions) WithSpokeKubeconfig(KubeConfig *rest.Config) *AgentOptions {
	o.KubeConfig = KubeConfig
	return o
}

func (o *AgentOptions) WithBootstrapKubeconfig(bootstrapKubeconfig string) *AgentOptions {
	o.RegistrationAgent.BootstrapKubeconfig = bootstrapKubeconfig
	return o
}

func (o *AgentOptions) WithHubKubeconfigDir(hubKubeconfigDir string) *AgentOptions {
	o.RegistrationAgent.HubKubeconfigDir = hubKubeconfigDir
	return o
}

func (o *AgentOptions) Complete() error {
	if o.KubeConfig != nil {
		return nil
	}

	if o.RegistrationAgent.SpokeKubeconfig == "" {
		KubeConfig, err := rest.InClusterConfig()
		if err != nil {
			return err
		}

		o.KubeConfig = KubeConfig
		return nil
	}

	KubeConfig, err := clientcmd.BuildConfigFromFlags("", o.RegistrationAgent.SpokeKubeconfig)
	if err != nil {
		return err
	}

	o.KubeConfig = KubeConfig
	return nil
}

func (o *AgentOptions) Validate() error {
	return nil
}

func (o *AgentOptions) RunAgent(ctx context.Context) error {
	if err := o.Complete(); err != nil {
		return err
	}

	if err := o.Validate(); err != nil {
		return err
	}

	o.KubeConfig.QPS = o.QPS
	o.KubeConfig.Burst = o.Burst

	apiExtensionsClient, err := apiextensionsclient.NewForConfig(o.KubeConfig)
	if err != nil {
		return err
	}

	if err := o.ensureCRDs(ctx, apiExtensionsClient); err != nil {
		return err
	}

	klog.Infof("Starting registration agent")
	go func() {
		ctrlContext := &controllercmd.ControllerContext{
			KubeConfig:    o.KubeConfig,
			EventRecorder: o.eventRecorder,
		}

		if err := o.RegistrationAgent.RunSpokeAgent(ctx, ctrlContext); err != nil {
			klog.Fatalf("failed to run registration agent, %v", err)
		}
	}()

	klog.Infof("Waiting for hub kubeconfig...")
	kubeconfigPath := path.Join(o.RegistrationAgent.HubKubeconfigDir, clientcert.KubeconfigFile)
	if err := o.waitForValidHubKubeConfig(ctx, kubeconfigPath); err != nil {
		klog.Fatalf("failed to wait hub kubeconfig, %v", err)
	}

	hubRestConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return err
	}

	//TODO also need update the appliedmanifestworks finalizer when we stop this pod
	klog.Infof("Starting work agent")
	if err := o.startWorkControllers(ctx, hubRestConfig, o.KubeConfig, o.eventRecorder); err != nil {
		klog.Fatalf("failed to run work agent, %v", err)
	}

	<-ctx.Done()
	return nil
}

func (o *AgentOptions) ensureCRDs(ctx context.Context, client apiextensionsclient.Interface) error {
	for _, crdFileName := range crdStaticFiles {
		template, err := crds.ReadFile(crdFileName)
		if err != nil {
			return err
		}

		objData := assets.MustCreateAssetFromTemplate(crdFileName, template, nil).Data
		obj, _, err := genericCodec.Decode(objData, nil, nil)
		if err != nil {
			return err
		}

		switch required := obj.(type) {
		case *crdv1.CustomResourceDefinition:
			if _, _, err := resourceapply.ApplyCustomResourceDefinitionV1(
				ctx,
				client.ApiextensionsV1(),
				o.eventRecorder,
				required,
			); err != nil {
				return err
			}
		}
	}

	return nil
}

func (o *AgentOptions) waitForValidHubKubeConfig(ctx context.Context, kubeconfigPath string) error {
	return wait.PollImmediateInfinite(
		5*time.Second,
		func() (bool, error) {
			if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
				klog.V(4).Infof("Kubeconfig file %q not found", kubeconfigPath)
				return false, nil
			}

			keyPath := path.Join(o.RegistrationAgent.HubKubeconfigDir, clientcert.TLSKeyFile)
			if _, err := os.Stat(keyPath); os.IsNotExist(err) {
				klog.V(4).Infof("TLS key file %q not found", keyPath)
				return false, nil
			}

			certPath := path.Join(o.RegistrationAgent.HubKubeconfigDir, clientcert.TLSCertFile)
			certData, err := os.ReadFile(path.Clean(certPath))
			if err != nil {
				klog.V(4).Infof("Unable to load TLS cert file %q", certPath)
				return false, nil
			}

			// check if the tls certificate is issued for the current cluster/agent
			clusterName, agentName, err := managedcluster.GetClusterAgentNamesFromCertificate(certData)
			if err != nil {
				return false, nil
			}

			if clusterName != o.RegistrationAgent.ClusterName || agentName != o.RegistrationAgent.AgentName {
				klog.V(4).Infof("Certificate in file %q is issued for agent %q instead of %q",
					certPath, fmt.Sprintf("%s:%s", clusterName, agentName),
					fmt.Sprintf("%s:%s", o.RegistrationAgent.ClusterName, o.RegistrationAgent.AgentName))
				return false, nil
			}

			return clientcert.IsCertificateValid(certData, nil)
		},
	)
}

func (o *AgentOptions) startWorkControllers(ctx context.Context,
	hubRestConfig, spokeRestConfig *rest.Config, eventRecorder events.Recorder) error {
	hubhash := helper.HubHash(hubRestConfig.Host)
	agentID := hubhash

	hubWorkClient, err := workclientset.NewForConfig(hubRestConfig)
	if err != nil {
		return err
	}

	spokeDynamicClient, err := dynamic.NewForConfig(spokeRestConfig)
	if err != nil {
		return err
	}

	spokeKubeClient, err := kubernetes.NewForConfig(spokeRestConfig)
	if err != nil {
		return err
	}

	spokeAPIExtensionClient, err := apiextensionsclient.NewForConfig(spokeRestConfig)
	if err != nil {
		return err
	}

	spokeWorkClient, err := workclientset.NewForConfig(spokeRestConfig)
	if err != nil {
		return err
	}

	restMapper, err := apiutil.NewDynamicRESTMapper(spokeRestConfig, apiutil.WithLazyDiscovery)
	if err != nil {
		return err
	}

	// Only watch the cluster namespace on hub
	workInformerFactory := workinformers.NewSharedInformerFactoryWithOptions(
		hubWorkClient, 5*time.Minute, workinformers.WithNamespace(o.RegistrationAgent.ClusterName))
	spokeWorkInformerFactory := workinformers.NewSharedInformerFactory(spokeWorkClient, 5*time.Minute)

	validator := auth.NewFactory(
		spokeRestConfig,
		spokeKubeClient,
		workInformerFactory.Work().V1().ManifestWorks(),
		o.RegistrationAgent.ClusterName,
		eventRecorder,
		restMapper,
	).NewExecutorValidator(ctx, features.DefaultSpokeMutableFeatureGate.Enabled(feature.ExecutorValidatingCaches))

	manifestWorkController := manifestcontroller.NewManifestWorkController(
		eventRecorder,
		spokeDynamicClient,
		spokeKubeClient,
		spokeAPIExtensionClient,
		hubWorkClient.WorkV1().ManifestWorks(o.RegistrationAgent.ClusterName),
		workInformerFactory.Work().V1().ManifestWorks(),
		workInformerFactory.Work().V1().ManifestWorks().Lister().ManifestWorks(o.RegistrationAgent.ClusterName),
		spokeWorkClient.WorkV1().AppliedManifestWorks(),
		spokeWorkInformerFactory.Work().V1().AppliedManifestWorks(),
		hubhash, agentID,
		restMapper,
		validator,
	)

	addFinalizerController := finalizercontroller.NewAddFinalizerController(
		eventRecorder,
		hubWorkClient.WorkV1().ManifestWorks(o.RegistrationAgent.ClusterName),
		workInformerFactory.Work().V1().ManifestWorks(),
		workInformerFactory.Work().V1().ManifestWorks().Lister().ManifestWorks(o.RegistrationAgent.ClusterName),
	)

	appliedManifestWorkFinalizeController := finalizercontroller.NewAppliedManifestWorkFinalizeController(
		eventRecorder,
		spokeDynamicClient,
		spokeWorkClient.WorkV1().AppliedManifestWorks(),
		spokeWorkInformerFactory.Work().V1().AppliedManifestWorks(),
		agentID,
	)

	manifestWorkFinalizeController := finalizercontroller.NewManifestWorkFinalizeController(
		eventRecorder,
		hubWorkClient.WorkV1().ManifestWorks(o.RegistrationAgent.ClusterName),
		workInformerFactory.Work().V1().ManifestWorks(),
		workInformerFactory.Work().V1().ManifestWorks().Lister().ManifestWorks(o.RegistrationAgent.ClusterName),
		spokeWorkClient.WorkV1().AppliedManifestWorks(),
		spokeWorkInformerFactory.Work().V1().AppliedManifestWorks(),
		hubhash,
	)

	unmanagedAppliedManifestWorkController := finalizercontroller.NewUnManagedAppliedWorkController(
		eventRecorder,
		workInformerFactory.Work().V1().ManifestWorks(),
		workInformerFactory.Work().V1().ManifestWorks().Lister().ManifestWorks(o.RegistrationAgent.ClusterName),
		spokeWorkClient.WorkV1().AppliedManifestWorks(),
		spokeWorkInformerFactory.Work().V1().AppliedManifestWorks(),
		o.AppliedManifestWorkEvictionGracePeriod,
		hubhash, agentID,
	)

	appliedManifestWorkController := appliedmanifestcontroller.NewAppliedManifestWorkController(
		eventRecorder,
		spokeDynamicClient,
		hubWorkClient.WorkV1().ManifestWorks(o.RegistrationAgent.ClusterName),
		workInformerFactory.Work().V1().ManifestWorks(),
		workInformerFactory.Work().V1().ManifestWorks().Lister().ManifestWorks(o.RegistrationAgent.ClusterName),
		spokeWorkClient.WorkV1().AppliedManifestWorks(),
		spokeWorkInformerFactory.Work().V1().AppliedManifestWorks(),
		hubhash,
	)

	availableStatusController := statuscontroller.NewAvailableStatusController(
		eventRecorder,
		spokeDynamicClient,
		hubWorkClient.WorkV1().ManifestWorks(o.RegistrationAgent.ClusterName),
		workInformerFactory.Work().V1().ManifestWorks(),
		workInformerFactory.Work().V1().ManifestWorks().Lister().ManifestWorks(o.RegistrationAgent.ClusterName),
		o.StatusSyncInterval,
	)

	go workInformerFactory.Start(ctx.Done())
	go spokeWorkInformerFactory.Start(ctx.Done())
	go addFinalizerController.Run(ctx, 1)
	go appliedManifestWorkFinalizeController.Run(ctx, 1)
	go unmanagedAppliedManifestWorkController.Run(ctx, 1)
	go appliedManifestWorkController.Run(ctx, 1)
	go manifestWorkController.Run(ctx, 1)
	go manifestWorkFinalizeController.Run(ctx, 1)
	go availableStatusController.Run(ctx, 1)
	return nil
}
