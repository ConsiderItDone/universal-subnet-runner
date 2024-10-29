package main

import (
	"context"
	_ "embed"
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
	subnetSize     = 3
)

var (
	goPath = os.Getenv("GOPATH")

	home = os.Getenv("HOME")

	vmName = flag.String("vm-name", "subnetevm", "name of vm to be deployed")

	amountOfSubnets = flag.Int("amount-of-subnets", 1, "amount of subnets to be deployed")

	pluginID = flag.String("plugin-id", "srEXiWaHuhNyGwPUi444Tu47ZEDwxTWrbQiuD7FmgSAQ6X7Dy", "ID of vm plugin in cb58 format")

	//go:embed data/genesis
	genesis []byte
)

// Blocks until a signal is received on [signalChan], upon which
// [n.Stop()] is called. If [signalChan] is closed, does nothing.
// Closes [closedOnShutdownChan] amd [signalChan] when done shutting down network.
// This function should only be called once.
func shutdownOnSignal(
	log logging.Logger,
	shutdownSignal chan os.Signal,
) {
	sig := <-shutdownSignal
	log.Info("got OS signal", zap.Stringer("signal", sig))
	signal.Reset()
	close(shutdownSignal)
}

// Shows example usage of the Avalanche Network Runner.
// Creates a local five node Avalanche network
// and waits for all nodes to become healthy.
// The network runs until the user provides a SIGINT or SIGTERM.
func main() {
	flag.Parse()
	ctx := context.Background()
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

	binaryPath := fmt.Sprintf("%s/universal-subnet-runner/avalanchego", home)
	workDir := fmt.Sprintf("%s/universal-subnet-runner/networks/%d/nodes", home, time.Now().Unix())

	os.RemoveAll(workDir)
	os.MkdirAll(workDir, 0777)

	if err := run(ctx, log, *amountOfSubnets, binaryPath, workDir); err != nil {
		log.Fatal("fatal error", zap.Error(err))
		os.Exit(1)
	}
}

func await(ctx context.Context, nw network.Network, log logging.Logger, timeout time.Duration) error {
	// Wait until the nodes in the network are ready
	ctx, cancel := context.WithTimeout(ctx, timeout)
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

func run(ctx context.Context, log logging.Logger, amountOfSubnets int, binaryPath string, workDir string) error {
	// When we get a SIGINT or SIGTERM, stop the network and close [closedOnShutdownCh]
	shutdownSignal := make(chan os.Signal, 1)
	signal.Notify(shutdownSignal, syscall.SIGINT, syscall.SIGTERM)

	// Create the network
	nwConfig, err := local.NewDefaultConfigNNodes(fmt.Sprintf("%s/avalanchego", binaryPath), uint32(subnetSize*amountOfSubnets))
	if err != nil {
		return err
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

	// Wait until the nodes in the network are ready
	if err := await(ctx, nw, log, healthyTimeout); err != nil {
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

	blockchainSpecs := make([]network.BlockchainSpec, 0, amountOfSubnets)
	for i := 0; i < amountOfSubnets; i++ {
		subnetSpec := network.SubnetSpec{Participants: nodeNames[i*subnetSize : (i+1)*subnetSize]}
		blockchainSpecs = append(blockchainSpecs, network.BlockchainSpec{
			VMName:      *vmName,
			Genesis:     genesis,
			ChainConfig: []byte(`{"warp-api-enabled": true}`),
			SubnetSpec:  &subnetSpec,
		})
	}

	chains, err := nw.CreateBlockchains(ctx, blockchainSpecs)
	if err != nil {
		return err
	}

	// Wait until the nodes in the network are ready
	if err := await(ctx, nw, log, healthyTimeout); err != nil {
		return err
	}

	rpcUrls := make([]string, len(nodeNames))
	for i := range nodeNames {
		node, err := nw.GetNode(nodeNames[i])
		if err != nil {
			return err
		}
		rpcUrls[i] = fmt.Sprintf("http://127.0.0.1:%d/ext/bc/%s/rpc", node.GetAPIPort(), chains[i/subnetSize])
		log.Info("subnet rpc url", zap.String("node", nodeNames[i]), zap.String("url", rpcUrls[i]))
	}

	log.Info("Network will run until you CTRL + C to exit...")

	shutdownOnSignal(log, shutdownSignal)

	return nil
}
