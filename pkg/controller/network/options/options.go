package options

import (
	"fmt"
	"github.com/spf13/pflag"
)

type NetworkOptions struct {
	NetworkProvider string `json:"networkprovider" yaml:"networkProvider"`
}

func NewNetworkOptions() *NetworkOptions {
	return &NetworkOptions{
		NetworkProvider: "calico",
	}
}

func (s *NetworkOptions) ApplyTo(options *NetworkOptions) {
	if options == nil {
		options = s
		return
	}

	if s.NetworkProvider != "" {
		options.NetworkProvider = s.NetworkProvider
	}
}

func (s *NetworkOptions) Validate() []error {
	errors := make([]error, 0)

	if s.NetworkProvider != "calico" {
		errors = append(errors, fmt.Errorf("Invalid network provider %s", s.NetworkProvider))
	}

	return errors
}

func (s *NetworkOptions) AddFlags(fs *pflag.FlagSet, c *NetworkOptions) {
	fs.StringVar(&s.NetworkProvider, "networkprovider", c.NetworkProvider, "Kubesphere backend network plugin")
}
