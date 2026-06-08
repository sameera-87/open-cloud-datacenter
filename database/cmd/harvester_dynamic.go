//go:build hv005_dynamic_client

package main

import (
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func newHarvesterClient(restConfig *rest.Config, grafanaURL, mgmtLogicalSwitch string) (harvester.ClientInterface, error) {
	dynClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}
	hvClient := harvester.NewClient(dynClient, grafanaURL)
	hvClient.MgmtLogicalSwitch = mgmtLogicalSwitch
	return hvClient, nil
}
