package harvester

import (
	harvesterbuilder "github.com/harvester/harvester/pkg/builder"
	harvesterclientset "github.com/harvester/harvester/pkg/generated/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
)

// TypedClient is a dependency spike for using Harvester's generated clientset
// and VM builder helpers behind Interface.
type TypedClient struct {
	Clientset  harvesterclientset.Interface
	GrafanaURL string
}

func NewTypedClient(config *rest.Config, grafanaURL string) (*TypedClient, error) {
	clientset, err := harvesterclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &TypedClient{Clientset: clientset, GrafanaURL: grafanaURL}, nil
}

func buildTypedDependencyProbeVM() (any, error) {
	storageClassName := "harvester-longhorn"
	return harvesterbuilder.NewVMBuilder("dbaas-operator").
		Name("pg-dependency-probe").
		Namespace("default").
		CPU(1).
		Memory("1Gi").
		Run(true).
		PVCDisk("os-disk", harvesterbuilder.DiskBusVirtio, false, false, 1, "20Gi", "pg-dependency-probe-os", &harvesterbuilder.PersistentVolumeClaimOption{
			ImageID:          "default/ubuntu",
			VolumeMode:       corev1.PersistentVolumeBlock,
			AccessMode:       corev1.ReadWriteMany,
			StorageClassName: &storageClassName,
		}).
		CloudInitDisk(harvesterbuilder.CloudInitDiskName, harvesterbuilder.DiskBusVirtio, false, 0, harvesterbuilder.CloudInitSource{
			CloudInitType:         harvesterbuilder.CloudInitTypeNoCloud,
			UserDataSecretName:    "pg-dependency-probe-credentials",
			NetworkDataSecretName: "pg-dependency-probe-credentials",
		}).
		VM()
}
