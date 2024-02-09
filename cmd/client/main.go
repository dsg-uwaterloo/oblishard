package main

import (
	"context"
	"flag"
	"os"
	"path"
	"time"

	"github.com/dsg-uwaterloo/oblishard/pkg/client"
	"github.com/dsg-uwaterloo/oblishard/pkg/config"
	"github.com/dsg-uwaterloo/oblishard/pkg/tracing"
	"github.com/dsg-uwaterloo/oblishard/pkg/utils"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
)

// Usage: go run . -duration=<duration in seconds>  -logpath=<log path> -conf=<configs path>
func main() {
	logPath := flag.String("logpath", "", "path to write logs")
	configsPath := flag.String("conf", "../../configs/default", "configs directory path")
	duration := flag.Int("duration", 10, "duration of the experiment in seconds")
	outputFilePath := flag.String("output", "", "output file path")
	flag.Parse()
	parameters, err := config.ReadParameters(path.Join(*configsPath, "parameters.yaml"))
	if err != nil {
		os.Exit(1)
	}

	utils.InitLogging(parameters.Log, *logPath)

	routerEndpoints, err := config.ReadRouterEndpoints(path.Join(*configsPath, "router_endpoints.yaml"))
	if err != nil {
		log.Fatal().Msgf("Cannot read router endpoints from yaml file; %v", err)
	}

	redisEndpoints, err := config.ReadRedisEndpoints(path.Join(*configsPath, "redis_endpoints.yaml"))
	if err != nil {
		log.Fatal().Msgf("Cannot read redis endpoints from yaml file; %v", err)
	}

	rpcClients, err := client.StartRouterRPCClients(routerEndpoints)
	if err != nil {
		log.Fatal().Msgf("Failed to start clients; %v", err)
	}

	requests, err := client.ReadTraceFile(path.Join(*configsPath, "trace.txt"), parameters.BlockSize)
	if err != nil {
		log.Fatal().Msgf("Failed to read trace file; %v", err)
	}

	tracingProvider, err := tracing.NewProvider(context.Background(), "client", "localhost:4317", !parameters.Trace)
	if err != nil {
		log.Fatal().Msgf("Failed to create tracing provider; %v", err)
	}
	stopTracingProvider, err := tracingProvider.RegisterAsGlobal()
	if err != nil {
		log.Fatal().Msgf("Failed to register tracing provider; %v", err)
	}
	defer stopTracingProvider(context.Background())

	tracer := otel.Tracer("")

	c := client.NewClient(client.NewRateLimit(parameters.MaxRequests), tracer, rpcClients, requests)
	err = c.WaitForStorageToBeReady(redisEndpoints, parameters)
	if err != nil {
		log.Fatal().Msgf("Failed to check if storages are ready; %v", err)
	}

	readResponseChannel := make(chan client.ReadResponse)
	writeResponseChannel := make(chan client.WriteResponse)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*duration)*time.Second)
	defer cancel()

	go c.SendRequestsForever(ctx, readResponseChannel, writeResponseChannel)
	startTime := time.Now()
	readOperations, writeOperations := c.GetResponsesForever(ctx, readResponseChannel, writeResponseChannel)
	elapsed := time.Since(startTime)
	throughput := float64(readOperations+writeOperations) / elapsed.Seconds()
	averageLatency := float64(elapsed.Milliseconds()) / float64((readOperations + writeOperations))
	err = client.WriteOutputToFile(*outputFilePath, throughput, averageLatency)
	if err != nil {
		log.Fatal().Msgf("Failed to write output to file; %v", err)
	}
}
