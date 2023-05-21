package router

import (
	"context"
	"fmt"
	"log"
	"math"
	"net"

	pb "github.com/dsg-uwaterloo/oblishard/api/router"
	shardnodepb "github.com/dsg-uwaterloo/oblishard/api/shardnode"
	utils "github.com/dsg-uwaterloo/oblishard/pkg/utils"
	"google.golang.org/grpc"
)

type routerServer struct {
	pb.UnimplementedRouterServer
	shardNodeRPCClients map[int]RPCClient //TODO maybe the name layerTwoRPCClients is a bit misleading but I don't know
}

func (r *routerServer) whereToForward(block string) (shardNodeID int) {
	h := utils.Hash(block)
	return int(math.Mod(float64(h), float64(len(r.shardNodeRPCClients))))
}

func (r *routerServer) Read(ctx context.Context, readRequest *pb.ReadRequest) (*pb.ReadReply, error) {
	whereToForward := r.whereToForward(readRequest.Block)
	shardNodeRPCClient := r.shardNodeRPCClients[whereToForward]

	shardNodeRPCClient.ClientAPI.Read(context.Background(),
		&shardnodepb.ReadRequest{Block: readRequest.Block})
	return &pb.ReadReply{Value: "test"}, nil
}

func (r *routerServer) Write(ctx context.Context, writeRequest *pb.WriteRequest) (*pb.WriteReply, error) {
	whereToForward := r.whereToForward(writeRequest.Block)
	shardNodeRPCClient := r.shardNodeRPCClients[whereToForward]

	shardNodeRPCClient.ClientAPI.Write(context.Background(),
		&shardnodepb.WriteRequest{Block: writeRequest.Block, Value: writeRequest.Value})
	return &pb.WriteReply{Success: true}, nil
}

func StartRPCServer(shardNodeRPCClients map[int]RPCClient) {
	lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", 8745)) //TODO change this to use env vars or other dynamic mechanisms
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	pb.RegisterRouterServer(grpcServer,
		&routerServer{shardNodeRPCClients: shardNodeRPCClients})
	grpcServer.Serve(lis)
}
