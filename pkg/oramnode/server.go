package oramnode

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"time"

	pb "github.com/dsg-uwaterloo/oblishard/api/oramnode"
	shardnodepb "github.com/dsg-uwaterloo/oblishard/api/shardnode"
	"github.com/dsg-uwaterloo/oblishard/pkg/rpc"
	"github.com/dsg-uwaterloo/oblishard/pkg/storage"
	"github.com/hashicorp/raft"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type oramNodeServer struct {
	pb.UnimplementedOramNodeServer
	oramNodeServerID    int
	replicaID           int
	raftNode            *raft.Raft
	oramNodeFSM         *oramNodeFSM
	shardNodeRPCClients map[int]ReplicaRPCClientMap
}

func newOramNodeServer(oramNodeServerID int, replicaID int, raftNode *raft.Raft, oramNodeFSM *oramNodeFSM, shardNodeRPCClients map[int]ReplicaRPCClientMap) *oramNodeServer {
	return &oramNodeServer{
		oramNodeServerID:    oramNodeServerID,
		replicaID:           replicaID,
		raftNode:            raftNode,
		oramNodeFSM:         oramNodeFSM,
		shardNodeRPCClients: shardNodeRPCClients,
	}
}

func (o *oramNodeServer) earlyReshuffle(path int, storageID int) error {
	for level := 0; level < storage.LevelCount; level++ {
		accessCount, err := storage.GetAccessCount(level, path, storageID)
		if err != nil {
			return fmt.Errorf("unable to get access count from the server; %s", err)
		}
		if accessCount < storage.MaxAccessCount {
			continue
		}
		localStash, err := storage.ReadBucket(level, path, storageID)
		if err != nil {
			return fmt.Errorf("unable to read bucket from the server; %s", err)
		}
		writtenBlocks, err := storage.WriteBucket(level, path, storageID, localStash)
		if err != nil {
			return fmt.Errorf("unable to write bucket from the server; %s", err)
		}
		for block := range localStash {
			if _, exists := writtenBlocks[block]; !exists {
				return fmt.Errorf("unable to write all blocks to the bucket")
			}
		}
	}
	return nil
}

func (o *oramNodeServer) getBlocksFromShardNode(replicas ReplicaRPCClientMap, path int, storageID int) ([]*shardnodepb.Block, error) {

	type sendBlocksResult struct {
		reply *shardnodepb.SendBlocksReply
		err   error
	}
	responseChannel := make(chan sendBlocksResult)
	for _, client := range replicas {
		go func(client ShardNodeRPCClient) {
			reply, err := client.ClientAPI.SendBlocks(
				context.Background(),
				&shardnodepb.SendBlocksRequest{
					MaxBlocks: 5, //TODO: read as a config variable
					Path:      int32(path),
					StorageId: int32(storageID),
				},
			)
			responseChannel <- sendBlocksResult{reply: reply, err: err}
		}(client)
	}

	timeout := time.After(2 * time.Second)
	for {
		select {
		case sendBlocksResult := <-responseChannel:
			if sendBlocksResult.err == nil {
				return sendBlocksResult.reply.Blocks, nil
			}
		case <-timeout:
			return nil, fmt.Errorf("could not read blocks from the shardnode")
		}
	}
}

func (o *oramNodeServer) evict(path int, storageID int) error {
	beginEvictionCommand, err := newReplicateBeginEvictionCommand(path, storageID)
	if err != nil {
		return fmt.Errorf("unable to marshal begin eviction command; %s", err)
	}
	err = o.raftNode.Apply(beginEvictionCommand, 2*time.Second).Error()
	if err != nil {
		return fmt.Errorf("could not apply log to the FSM; %s", err)
	}

	aggStash := make(map[string]string)
	for level := 0; level < storage.LevelCount; level++ {
		blocks, err := storage.ReadBucket(level, path, storageID)
		if err != nil {
			return fmt.Errorf("unable to read bucket; %s", err)
		}
		for block, value := range blocks {
			aggStash[block] = value
		}
		//TODO: get blocks from multiple random shard nodes
		randomShardNode := o.shardNodeRPCClients[0]
		shardNodeBlocks, err := o.getBlocksFromShardNode(randomShardNode, path, storageID)
		if err != nil {
			return fmt.Errorf("unable to get blocks from shard node; %s", err)
		}
		for _, block := range shardNodeBlocks {
			aggStash[block.Block] = block.Value
		}
	}

	for level := storage.LevelCount - 1; level >= 0; level-- {
		writtenBlocks, err := storage.AtomicWriteBucket(level, path, storageID, aggStash)
		if err != nil {
			return fmt.Errorf("unable to atomic write bucket; %s", err)
		}
		for block := range writtenBlocks {
			delete(aggStash, block)
		}
	}

	//TODO: send back acks and nacks
	//TODO: replicate end eviction

	return nil
}

func (o *oramNodeServer) ReadPath(ctx context.Context, request *pb.ReadPathRequest) (*pb.ReadPathReply, error) {
	if o.raftNode.State() != raft.Leader {
		return nil, fmt.Errorf("not the leader node")
	}

	md, _ := metadata.FromIncomingContext(ctx)
	requestID := md["requestid"][0]

	var offsetList []int
	for level := 0; level < storage.LevelCount; level++ {
		offset, err := storage.GetBlockOffset(level, int(request.Path), int(request.StorageId), request.Block)
		if err != nil {
			return nil, fmt.Errorf("could not get offset from storage")
		}
		offsetList = append(offsetList, offset)
	}
	replicateOffsetAndBeginReadPathCommand, err := newReplicateOffsetListCommand(requestID, offsetList)
	if err != nil {
		return nil, fmt.Errorf("could not create offsetList replication command; %s", err)
	}
	err = o.raftNode.Apply(replicateOffsetAndBeginReadPathCommand, 2*time.Second).Error()
	if err != nil {
		return nil, fmt.Errorf("could not apply log to the FSM; %s", err)
	}

	var returnValue string
	for level := 0; level < storage.LevelCount; level++ {
		value, err := storage.ReadBlock(level, int(request.Path), offsetList[level], int(request.StorageId))
		if err == nil {
			returnValue = value
		}
	}

	err = o.earlyReshuffle(int(request.Path), int(request.StorageId))
	if err != nil { //TODO: should we delete offsetList in case of an earlyReshuffle error?
		return nil, fmt.Errorf("early reshuffle failed;%s", err)
	}

	replicateDeleteOffsetListCommand, err := newReplicateDeleteOffsetListCommand(requestID)
	if err != nil {
		return nil, fmt.Errorf("could not create delete offsetList replication command; %s", err)
	}
	err = o.raftNode.Apply(replicateDeleteOffsetListCommand, 2*time.Second).Error()
	if err != nil {
		return nil, fmt.Errorf("could not apply log to the FSM; %s", err)
	}

	return &pb.ReadPathReply{Value: returnValue}, nil
}

func (o *oramNodeServer) JoinRaftVoter(ctx context.Context, joinRaftVoterRequest *pb.JoinRaftVoterRequest) (*pb.JoinRaftVoterReply, error) {
	requestingNodeId := joinRaftVoterRequest.NodeId
	requestingNodeAddr := joinRaftVoterRequest.NodeAddr

	log.Printf("received join request from node %d at %s", requestingNodeId, requestingNodeAddr)

	err := o.raftNode.AddVoter(
		raft.ServerID(strconv.Itoa(int(requestingNodeId))),
		raft.ServerAddress(requestingNodeAddr),
		0, 0).Error()

	if err != nil {
		return &pb.JoinRaftVoterReply{Success: false}, fmt.Errorf("voter could not be added to the leader; %s", err)
	}
	return &pb.JoinRaftVoterReply{Success: true}, nil
}

func StartServer(oramNodeServerID int, rpcPort int, replicaID int, raftPort int, joinAddr string, shardNodeRPCClients map[int]ReplicaRPCClientMap) {
	isFirst := joinAddr == ""
	oramNodeFSM := newOramNodeFSM()
	r, err := startRaftServer(isFirst, replicaID, raftPort, oramNodeFSM)
	if err != nil {
		log.Fatalf("The raft node creation did not succeed; %s", err)
	}

	go func() {
		for {
			time.Sleep(5 * time.Second)
			fmt.Println(oramNodeFSM)
		}
	}()

	if !isFirst {
		conn, err := grpc.Dial(joinAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatalf("The raft node could not connect to the leader as a new voter; %s", err)
		}
		client := pb.NewOramNodeClient(conn)
		fmt.Println(raftPort)
		joinRaftVoterReply, err := client.JoinRaftVoter(
			context.Background(),
			&pb.JoinRaftVoterRequest{
				NodeId:   int32(replicaID),
				NodeAddr: fmt.Sprintf("localhost:%d", raftPort),
			},
		)
		if err != nil || !joinRaftVoterReply.Success {
			log.Fatalf("The raft node could not connect to the leader as a new voter; %s", err)
		}
	}

	//TODO: init raft node and join on joinAddr
	lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", rpcPort))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	oramNodeServer := newOramNodeServer(oramNodeServerID, replicaID, r, oramNodeFSM, shardNodeRPCClients)
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(rpc.ContextPropagationUnaryServerInterceptor()))
	pb.RegisterOramNodeServer(grpcServer, oramNodeServer)
	grpcServer.Serve(lis)
}
