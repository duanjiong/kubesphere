package options

import (
	"flag"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog"
	kubesphereconfig "kubesphere.io/kubesphere/pkg/apiserver/config"
	"kubesphere.io/kubesphere/pkg/controller/network/options"
	"kubesphere.io/kubesphere/pkg/simple/client/k8s"
	"strings"
)

type NetworkManagerOptions struct {
	KubernetesOptions *k8s.KubernetesOptions
	Options           *options.NetworkOptions
}

func NewNetworkManagerOptions() *NetworkManagerOptions {
	s := &NetworkManagerOptions{
		KubernetesOptions: k8s.NewKubernetesOptions(),
		Options:           options.NewNetworkOptions(),
	}

	return s
}

func (s *NetworkManagerOptions) Validate() []error {
	var errs []error
	errs = append(errs, s.Options.Validate()...)
	errs = append(errs, s.KubernetesOptions.Validate()...)
	return errs
}

func (s *NetworkManagerOptions) ApplyTo(conf *kubesphereconfig.Config) {
	s.KubernetesOptions.ApplyTo(conf.KubernetesOptions)
}

func (s *NetworkManagerOptions) Flags() cliflag.NamedFlagSets {
	fss := cliflag.NamedFlagSets{}

	s.KubernetesOptions.AddFlags(fss.FlagSet("kubernetes"), s.KubernetesOptions)
	s.Options.AddFlags(fss.FlagSet("ks-network"), s.Options)

	kfs := fss.FlagSet("klog")
	local := flag.NewFlagSet("klog", flag.ExitOnError)
	klog.InitFlags(local)
	local.VisitAll(func(fl *flag.Flag) {
		fl.Name = strings.Replace(fl.Name, "_", "-", -1)
		kfs.AddGoFlag(fl)
	})

	return fss
}
