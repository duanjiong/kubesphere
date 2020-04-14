package main

import (
	"kubesphere.io/kubesphere/cmd/ks-network/app"
	"os"
)

func main() {
	command := app.NewNetworkCommand()

	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}
