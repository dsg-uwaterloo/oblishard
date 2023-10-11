package main

import (
	"context"
	"flag"
	"path"

	"github.com/dsg-uwaterloo/oblishard/pkg/config"
	oramnode "github.com/dsg-uwaterloo/oblishard/pkg/oramnode"
	"github.com/dsg-uwaterloo/oblishard/pkg/tracing"
	"github.com/dsg-uwaterloo/oblishard/pkg/utils"
	"github.com/rs/zerolog/log"
)

// Usage: ./oramnode -oramnodeid=<oramnodeid> -ip=<ip> -rpcport=<rpcport> -replicaid=<replicaid> -raftport=<raftport> -raftdir=<raftdir> -joinaddr=<ip:port> -conf=<configs path>
func main() {
	utils.InitLogging(true)

	oramNodeID := flag.Int("oramnodeid", 0, "oramnode id, starting consecutively from zero")
	ip := flag.String("ip", "", "ip of this replica")
	replicaID := flag.Int("replicaid", 0, "replica id, starting consecutively from zero")
	rpcPort := flag.Int("rpcport", 0, "node rpc port")
	raftPort := flag.Int("raftport", 0, "node raft port")
	raftDir := flag.String("raftdir", "", "the address of the raft snapshot directory")
	joinAddr := flag.String("joinaddr", "", "the address of the initial raft node, which bootstraped the cluster")
	configsPath := flag.String("conf", "", "configs directory path")
	flag.Parse()
	if *rpcPort == 0 {
		log.Fatal().Msgf("The rpc port should be provided with the -rpcport flag")
	}
	if *raftPort == 0 {
		log.Fatal().Msgf("The raft port should be provided with the -raftport flag")
	}
	shardNodeEndpoints, err := config.ReadShardNodeEndpoints(path.Join(*configsPath, "shardnode_endpoints.yaml"))
	if err != nil {
		log.Fatal().Msgf("Cannot read shard node endpoints from yaml file; %v", err)
	}
	rpcClients, err := oramnode.StartShardNodeRPCClients(shardNodeEndpoints)
	if err != nil {
		log.Fatal().Msgf("Failed to create client connections with shard node servers; %v", err)
	}

	parameters, err := config.ReadParameters(path.Join(*configsPath, "parameters.yaml"))
	if err != nil {
		log.Fatal().Msgf("Failed to read parameters from yaml file; %v", err)
	}

	// TODO: add a replica id to this
	// TODO: read the exporter url from a config file or sth like that
	tracingProvider, err := tracing.NewProvider(context.Background(), "oramnode", "localhost:4317")
	if err != nil {
		log.Fatal().Msgf("Failed to create tracing provider; %v", err)
	}
	stopTracingProvider, err := tracingProvider.RegisterAsGlobal()
	if err != nil {
		log.Fatal().Msgf("Failed to register tracing provider; %v", err)
	}
	defer stopTracingProvider(context.Background())

	oramnode.StartServer(*oramNodeID, *ip, *rpcPort, *replicaID, *raftPort, *raftDir, *joinAddr, rpcClients, parameters)
}
