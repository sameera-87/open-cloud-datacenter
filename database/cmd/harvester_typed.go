//go:build hv005_typed_client

package main

import (
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	"k8s.io/client-go/rest"
)

func newHarvesterClient(restConfig *rest.Config, grafanaURL, mgmtLogicalSwitch string) (harvester.ClientInterface, error) {
	hvClient, err := harvester.NewTypedClient(restConfig, grafanaURL)
	if err != nil {
		return nil, err
	}
	hvClient.MgmtLogicalSwitch = mgmtLogicalSwitch
	return hvClient, nil
}
