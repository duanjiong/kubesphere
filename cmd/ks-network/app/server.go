package app

import (
	"fmt"
	"github.com/spf13/cobra"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog"
	"kubesphere.io/kubesphere/cmd/ks-network/app/options"
	"kubesphere.io/kubesphere/pkg/apis"
	"kubesphere.io/kubesphere/pkg/controller/network/nsnetworkpolicy"
	"kubesphere.io/kubesphere/pkg/controller/network/provider"
	"kubesphere.io/kubesphere/pkg/informers"
	"kubesphere.io/kubesphere/pkg/simple/client/k8s"
	"kubesphere.io/kubesphere/pkg/utils/signals"
	"kubesphere.io/kubesphere/pkg/utils/term"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func NewNetworkCommand() *cobra.Command {
	s := options.NewNetworkManagerOptions()

	cmd := &cobra.Command{
		Use:  "ks-network",
		Long: `ks-network implement network policy and ippool etc.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if errs := s.Validate(); len(errs) != 0 {
				return utilerrors.NewAggregate(errs)
			}

			return Run(s, signals.SetupSignalHandler())
		},
	}

	fs := cmd.Flags()
	namedFlagSets := s.Flags()
	for _, f := range namedFlagSets.FlagSets {
		fs.AddFlagSet(f)
	}

	usageFmt := "Usage:\n  %s\n"
	cols, _, _ := term.TerminalSize(cmd.OutOrStdout())
	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n\n"+usageFmt, cmd.Long, cmd.UseLine())
		cliflag.PrintSections(cmd.OutOrStdout(), namedFlagSets, cols)
	})
	return cmd
}

func Run(s *options.NetworkManagerOptions, stopCh <-chan struct{}) error {
	kubernetesClient, err := k8s.NewKubernetesClient(s.KubernetesOptions)
	if err != nil {
		klog.Errorf("Failed to create kubernetes clientset %v", err)
		return err
	}

	informerFactory := informers.NewInformerFactories(kubernetesClient.Kubernetes(), kubernetesClient.KubeSphere(), kubernetesClient.Istio(), kubernetesClient.Application())

	klog.V(0).Info("setting up manager")
	mgr, err := manager.New(kubernetesClient.Config(), manager.Options{})
	if err != nil {
		klog.Fatalf("unable to set up overall controller manager: %v", err)
	}

	klog.V(0).Info("setting up scheme")
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		klog.Fatalf("unable add APIs to scheme: %v", err)
	}

	if s.Options.NetworkProvider == "calico" {
		calicoProvider, err := provider.NewCalicoNetworkProvider()
		if err != nil {
			klog.Errorf("Create Network provider error %s", err)
			return err
		}
		nsnpController := nsnetworkpolicy.NewnamespacenpController(kubernetesClient.Kubernetes(),
			kubernetesClient.KubeSphere().NetworkV1alpha1(),
			informerFactory.KubeSphereSharedInformerFactory().Network().V1alpha1().NamespaceNetworkPolicies(),
			informerFactory.KubernetesSharedInformerFactory().Core().V1().Services(),
			informerFactory.KubeSphereSharedInformerFactory().Tenant().V1alpha1().Workspaces(),
			informerFactory.KubernetesSharedInformerFactory().Core().V1().Namespaces(),
			calicoProvider)

		if err := mgr.Add(nsnpController); err != nil {
			return err
		}

		nsnetworkpolicy.RegisterWebhooks(mgr)
	}

	// Start cache data after all informer is registered
	informerFactory.Start(stopCh)

	klog.V(0).Info("Starting the controllers.")
	if err = mgr.Start(stopCh); err != nil {
		klog.Fatalf("unable to run the manager: %v", err)
	}

	select {}
}
