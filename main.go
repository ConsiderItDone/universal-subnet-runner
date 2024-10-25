package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"go/build"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ava-labs/avalanche-network-runner/local"
	"github.com/ava-labs/avalanche-network-runner/network"
	"github.com/ava-labs/avalanchego/utils/logging"
	"go.uber.org/zap"
)

const (
	healthyTimeout = 2 * time.Minute
	defaultShift   = 10
)

var (
	goPath = os.Getenv("GOPATH")

	home = os.Getenv("HOME")

	vmName = flag.String("vm-name", "subnetevm", "name of vm to be deployed")

	pluginID = flag.String("plugin-id", "srEXiWaHuhNyGwPUi444Tu47ZEDwxTWrbQiuD7FmgSAQ6X7Dy", "ID of vm plugin in cb58 format")

	configs []NetworkConfig

	//go:embed data/genesis
	genesis []byte
)

type NetworkConfig struct {
	NetworkID    uint32
	PortShifting int
}

// Blocks until a signal is received on [signalChan], upon which
// [n.Stop()] is called. If [signalChan] is closed, does nothing.
// Closes [closedOnShutdownChan] amd [signalChan] when done shutting down network.
// This function should only be called once.
func shutdownOnSignal(
	log logging.Logger,
	n network.Network,
	signalChan chan os.Signal,
	closedOnShutdownChan chan struct{},
) {
	sig := <-signalChan
	log.Info("got OS signal", zap.Stringer("signal", sig))
	if err := n.Stop(context.Background()); err != nil {
		log.Info("error stopping network", zap.Error(err))
	}
	signal.Reset()
	close(signalChan)
	close(closedOnShutdownChan)
}

// Shows example usage of the Avalanche Network Runner.
// Creates a local five node Avalanche network
// and waits for all nodes to become healthy.
// The network runs until the user provides a SIGINT or SIGTERM.
func main() {
	flag.Parse()
	// Create the logger
	logFactory := logging.NewFactory(logging.Config{
		DisplayLevel: logging.Info,
		LogLevel:     logging.Info,
	})
	log, err := logFactory.Make("main")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	if goPath == "" {
		goPath = build.Default.GOPATH
	}

	configFilePath := fmt.Sprintf("%s/universal-subnet-runner/config.json", home)

	// Check if the file exists
	if _, err = os.Stat(configFilePath); err == nil {
		// File exists, so read its content
		data, err := os.ReadFile(configFilePath)
		if err != nil {
			log.Error("Error reading file:", zap.Error(err))
			return
		}

		err = json.Unmarshal(data, &configs)
		if err != nil {
			log.Error("Error unmarshalling JSON:", zap.Error(err))
			return
		}
	}

	binaryPath := fmt.Sprintf("%s/universal-subnet-runner/avalanchego", home)
	workDir := fmt.Sprintf("%s/universal-subnet-runner/networks/%d/nodes", home, time.Now().Unix())

	os.RemoveAll(workDir)
	os.MkdirAll(workDir, 0777)

	if err := run(log, configFilePath, binaryPath, workDir); err != nil {
		log.Fatal("fatal error", zap.Error(err))
		os.Exit(1)
	}
}

func await(nw network.Network, log logging.Logger, timeout time.Duration) error {
	// Wait until the nodes in the network are ready
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	log.Info("waiting for all nodes to report healthy...")
	err := nw.Healthy(ctx)
	if err == nil {
		log.Info("all nodes healthy...")
	}
	return err
}

func copy(src, dst string) (int64, error) {
	sourceFileStat, err := os.Stat(src)
	if err != nil {
		return 0, err
	}

	if !sourceFileStat.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", src)
	}

	source, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer destination.Close()

	nBytes, err := io.Copy(destination, source)
	if err := os.Chmod(dst, 0777); err != nil {
		return 0, err
	}
	return nBytes, err
}

func run(log logging.Logger, configFilePath string, binaryPath string, workDir string) error {
	// Create the network
	nwConfig, err := local.NewDefaultConfig(fmt.Sprintf("%s/avalanchego", binaryPath))
	if err != nil {
		return err
	}
	shift := defaultShift * len(configs)
	for i := 0; i < len(nwConfig.NodeConfigs); i++ {
		nwConfig.NodeConfigs[i].Flags["http-port"] = nwConfig.NodeConfigs[i].Flags["http-port"].(int) + shift
		nwConfig.NodeConfigs[i].Flags["staking-port"] = nwConfig.NodeConfigs[i].Flags["staking-port"].(int) + shift
	}

	nwConfig.Flags["log-level"] = "INFO"

	nw, err := local.NewNetwork(log, nwConfig, workDir, "", true, false, true)
	if err != nil {
		return err
	}
	defer func() { // Stop the network when this function returns
		if err := nw.Stop(context.Background()); err != nil {
			log.Info("error stopping network", zap.Error(err))
		}
	}()

	networkID, err := nw.GetNetworkID()
	if err != nil {
		return err
	}

	// Add a new record to the slice
	networkConfig := NetworkConfig{NetworkID: networkID, PortShifting: shift}
	configs = append(configs, networkConfig)

	// Marshal the updated slice back to JSON
	updatedConfigContent, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		log.Error("Error marshalling JSON:", zap.Error(err))
		return err
	}

	// Write the updated JSON back to the file
	err = os.WriteFile(configFilePath, updatedConfigContent, 0644)
	if err != nil {
		log.Error("Error writing to file:", zap.Error(err))
		return err
	}

	// When we get a SIGINT or SIGTERM, stop the network and close [closedOnShutdownCh]
	signalsChan := make(chan os.Signal, 1)
	signal.Notify(signalsChan, syscall.SIGINT)
	signal.Notify(signalsChan, syscall.SIGTERM)
	closedOnShutdownCh := make(chan struct{})
	go func() {
		shutdownOnSignal(log, nw, signalsChan, closedOnShutdownCh)
	}()

	// Wait until the nodes in the network are ready
	if err := await(nw, log, healthyTimeout); err != nil {
		return err
	}

	// Add some chain
	nodeNames, err := nw.GetNodeNames()
	if err != nil {
		return err
	}

	for i := range nodeNames {
		node, err := nw.GetNode(nodeNames[i])
		if err != nil {
			return err
		}
		if _, err := copy(
			fmt.Sprintf("%s/plugins/%s", binaryPath, *pluginID),
			fmt.Sprintf("%s/plugins/%s", node.GetDataDir(), *pluginID),
		); err != nil {
			return err
		}
	}

	chains, err := nw.CreateBlockchains(context.Background(), []network.BlockchainSpec{
		{
			VMName:      *vmName,
			Genesis:     genesis,
			ChainConfig: []byte(`{"warp-api-enabled": true}`),
			SubnetSpec: &network.SubnetSpec{
				SubnetConfig: nil,
				Participants: nodeNames,
			},
		},
	})
	if err != nil {
		return err
	}

	// Wait until the nodes in the network are ready
	if err := await(nw, log, healthyTimeout); err != nil {
		return err
	}

	rpcUrls := make([]string, len(nodeNames))
	for i := range nodeNames {
		node, err := nw.GetNode(nodeNames[i])
		if err != nil {
			return err
		}
		rpcUrls[i] = fmt.Sprintf("http://127.0.0.1:%d/ext/bc/%s/rpc", node.GetAPIPort(), chains[0])
		log.Info("subnet rpc url", zap.String("node", nodeNames[i]), zap.String("url", rpcUrls[i]))
	}

	log.Info("Network will run until you CTRL + C to exit...")

	<-closedOnShutdownCh
	return nil
}
