/*
Copyright (C) 2016 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	units "github.com/docker/go-units"
	"github.com/docker/machine/libmachine"
	"github.com/docker/machine/libmachine/host"
	"github.com/golang/glog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	cfg "k8s.io/kubernetes/pkg/client/unversioned/clientcmd/api"

	"github.com/jimmidyson/minishift/pkg/minikube/cluster"
	"github.com/jimmidyson/minishift/pkg/minikube/constants"
	"github.com/jimmidyson/minishift/pkg/minikube/kubeconfig"
	"github.com/jimmidyson/minishift/pkg/util"
)

const (
	isoURL                = "iso-url"
	memory                = "memory"
	cpus                  = "cpus"
	humanReadableDiskSize = "disk-size"
	vmDriver              = "vm-driver"
	kubernetesVersion     = "kubernetes-version"
	hostOnlyCIDR          = "host-only-cidr"
	deployRegistry        = "deploy-registry"
	deployRouter          = "deploy-router"
)

var (
	dockerEnv        []string
	insecureRegistry []string
	registryMirror   []string
)

// startCmd represents the start command
var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Starts a local OpenShift cluster.",
	Long: `Starts a local OpenShift cluster using Virtualbox. This command
assumes you already have Virtualbox installed.`,
	Run: runStart,
}

func runStart(cmd *cobra.Command, args []string) {
	fmt.Println("Starting local OpenShift cluster...")
	api := libmachine.NewClient(constants.Minipath, constants.MakeMiniPath("certs"))
	defer api.Close()

	config := cluster.MachineConfig{
		MinikubeISO:      viper.GetString(isoURL),
		Memory:           viper.GetInt(memory),
		CPUs:             viper.GetInt(cpus),
		DiskSize:         calculateDiskSizeInMB(viper.GetString(humanReadableDiskSize)),
		VMDriver:         viper.GetString(vmDriver),
		DockerEnv:        dockerEnv,
		InsecureRegistry: insecureRegistry,
		RegistryMirror:   registryMirror,
		HostOnlyCIDR:     viper.GetString(hostOnlyCIDR),
		DeployRouter:     viper.GetBool(deployRouter),
		DeployRegistry:   viper.GetBool(deployRegistry),
	}

	var host *host.Host
	start := func() (err error) {
		host, err = cluster.StartHost(api, config)
		if err != nil {
			glog.Errorf("Error starting host: %s. Retrying.\n", err)
		}
		return err
	}
	err := util.Retry(3, start)
	if err != nil {
		glog.Errorln("Error starting host: ", err)
		os.Exit(1)
	}

	if err := cluster.UpdateCluster(host.Driver); err != nil {
		glog.Errorln("Error updating cluster: ", err)
		os.Exit(1)
	}

	kubeIP, err := host.Driver.GetIP()
	if err != nil {
		glog.Errorln("Error connecting to cluster: ", err)
		os.Exit(1)
	}
	if err := cluster.StartCluster(host, kubeIP, config); err != nil {
		glog.Errorln("Error starting cluster: ", err)
		os.Exit(1)
	}

	kubeHost, err := host.Driver.GetURL()
	if err != nil {
		glog.Errorln("Error connecting to cluster: ", err)
		os.Exit(1)
	}
	kubeHost = strings.Replace(kubeHost, "tcp://", "https://", -1)
	kubeHost = strings.Replace(kubeHost, ":2376", ":"+strconv.Itoa(constants.APIServerPort), -1)

	// setup kubeconfig
	name := constants.MinikubeContext
	certAuth, err := cluster.GetCA(host)
	if err != nil {
		glog.Errorln("Error setting up kubeconfig: ", err)
		os.Exit(1)
	}
	if err := setupKubeconfig(name, kubeHost, certAuth); err != nil {
		glog.Errorln("Error setting up kubeconfig: ", err)
		os.Exit(1)
	}
	fmt.Println("oc is now configured to use the cluster.")
	fmt.Println("Run this command to use the cluster: ")
	fmt.Println("oc login --username=admin --password=admin")
}

func calculateDiskSizeInMB(humanReadableDiskSize string) int {
	diskSize, err := units.FromHumanSize(humanReadableDiskSize)
	if err != nil {
		glog.Errorf("Invalid disk size: %s", err)
	}
	return int(diskSize / units.MB)
}

// setupKubeconfig reads config from disk, adds the minikube settings, and writes it back.
// activeContext is true when minikube is the CurrentContext
// If no CurrentContext is set, the given name will be used.
func setupKubeconfig(name, server, certAuth string) error {
	configFile := constants.KubeconfigPath

	// read existing config or create new if does not exist
	config, err := kubeconfig.ReadConfigOrNew(configFile)
	if err != nil {
		return err
	}

	clusterName := name
	cluster := cfg.NewCluster()
	cluster.Server = server
	cluster.CertificateAuthorityData = []byte(certAuth)
	config.Clusters[clusterName] = cluster

	// user
	userName := strings.Replace(server, "https://", "", -1)
	userName = "admin/" + strings.Replace(userName, ".", "-", -1)

	user := cfg.NewAuthInfo()
	config.AuthInfos[userName] = user

	// context
	contextName := name
	context := cfg.NewContext()
	context.Cluster = clusterName
	context.AuthInfo = userName
	config.Contexts[contextName] = context

	// Always set current context to minikube.
	config.CurrentContext = contextName

	// write back to disk
	if err := kubeconfig.WriteConfig(config, configFile); err != nil {
		return err
	}
	return nil
}

func init() {
	startCmd.Flags().String(isoURL, constants.DefaultIsoUrl, "Location of the minishift iso")
	startCmd.Flags().String(vmDriver, constants.DefaultVMDriver, fmt.Sprintf("VM driver is one of: %v", constants.SupportedVMDrivers))
	startCmd.Flags().Int(memory, constants.DefaultMemory, "Amount of RAM allocated to the minishift VM")
	startCmd.Flags().Int(cpus, constants.DefaultCPUS, "Number of CPUs allocated to the minishift VM")
	startCmd.Flags().String(humanReadableDiskSize, constants.DefaultDiskSize, "Disk size allocated to the minishift VM (format: <number>[<unit>], where unit = b, k, m or g)")
	startCmd.Flags().String(hostOnlyCIDR, "192.168.99.1/24", "The CIDR to be used for the minishift VM (only supported with Virtualbox driver)")
	startCmd.Flags().StringSliceVar(&dockerEnv, "docker-env", nil, "Environment variables to pass to the Docker daemon. (format: key=value)")
	startCmd.Flags().StringSliceVar(&insecureRegistry, "insecure-registry", []string{"172.30.0.0/16"}, "Insecure Docker registries to pass to the Docker daemon")
	startCmd.Flags().StringSliceVar(&registryMirror, "registry-mirror", nil, "Registry mirrors to pass to the Docker daemon")
	startCmd.Flags().Bool(deployRegistry, true, "Should the OpenShift internal Docker registry be deployed?")
	startCmd.Flags().Bool(deployRouter, false, "Should the OpenShift router be deployed?")

	viper.BindPFlags(startCmd.Flags())

	RootCmd.AddCommand(startCmd)
}
