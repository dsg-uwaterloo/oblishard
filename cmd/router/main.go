package main

import (
	"context"
	"flag"
	"log"

	"github.com/dsg-uwaterloo/oblishard/pkg/config"
	router "github.com/dsg-uwaterloo/oblishard/pkg/router"
	"github.com/dsg-uwaterloo/oblishard/pkg/tracing"
)

// Usage: ./router -routerid=<routerid> -port=<port>
func main() {
	routerID := flag.Int("routerid", 0, "router id, starting consecutively from zero")
	port := flag.Int("port", 0, "node port")
	flag.Parse()
	if *port == 0 {
		log.Fatalf("The port should be provided with the -port flag")
	}

	shardNodeEndpoints, err := config.ReadShardNodeEndpoints("../../configs/shardnode_endpoints.yaml")
	if err != nil {
		log.Fatalf("Cannot read shard node endpoints from yaml file; %v", err)
	}
	rpcClients, err := router.StartShardNodeRPCClients(shardNodeEndpoints)
	if err != nil {
		log.Fatalf("Failed to create client connections with shard node servers; %v", err)
	}

	// TODO: add a replica id to this
	// TODO: read the exporter url from a config file or sth like that
	tracingProvider, err := tracing.NewProvider(context.Background(), "router", "localhost:4317")
	if err != nil {
		log.Fatalf("Failed to create tracing provider; %v", err)
	}
	stopTracingProvider, err := tracingProvider.RegisterAsGlobal()
	if err != nil {
		log.Fatalf("Failed to register tracing provider; %v", err)
	}
	defer stopTracingProvider(context.Background())

	router.StartRPCServer(rpcClients, *routerID, *port)
}
