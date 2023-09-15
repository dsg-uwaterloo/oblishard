package main

// TODO: It might need to handle multiple storage shards.

import (
	"context"
	"strconv"
	"errors"
	"math/rand"
	"time"
	"fmt"
)

// MaxAccessCount is the maximum times we can access a bucket safely.
const (
	MaxAccessCount int = 8
	Z = 4
    S = 6
	shift = 1 // 2^shift children per node
	host string = "localhost:6379"
	numDB = 2
)

// StorageHandler is responsible for handling one or multiple storage shards.
type StorageHandler struct {
	maxAccessCount int
	host string
	db   []int
	key  [][]byte
}

func NewStorageHandler() *StorageHandler {
	return &StorageHandler{
		maxAccessCount: MaxAccessCount,
		host: host,
		db:   []int{1, 2},
		key:  [][]byte{[]byte("passphrasewhichneedstobe32bytes!"),[]byte("passphrasewhichneedstobe32bytes.")},
	}
}

func (s *StorageHandler) GetMaxAccessCount() int {
	return s.maxAccessCount
}

// It returns valid randomly chosen path and storageID.
func (s *StorageHandler) GetRandomPathAndStorageID() (path int, storageID int) {
	// TODO: implement
	return 0, 0
}

// It returns a block offset based on the blocks argument.
//
// If a real block is found, it returns isReal=true and the block id.
// If non of the "blocks" are in the bucket, it returns isReal=false
func (s *StorageHandler) GetBlockOffset(bucketID int, storageID int, blocks []string) (offset int, isReal bool, blockFound string, err error) {
	// TODO: implement
	blockMap := make(map[string]int)
	for i := 0; i < Z; i++ {
		pos, key, err := s.GetMetadata(bucketID, strconv.Itoa(i), storageID)
		if err != nil {
			return -1, false, "", err
		}
		blockMap[key] = pos
	}
	for _, block := range(blocks) {
		pos, exist := blockMap[block]
		if exist {
			return pos, true, block, nil
		}
	}
	return -1, false, "", err
}

// It returns the number of times a bucket was accessed.
// This is helpful to know when to do an early reshuffle.
func (s *StorageHandler) GetAccessCount(bucketID int, storageID int) (count int, err error) {
	client := s.getClient(storageID)
	ctx := context.Background()
	accessCountS, err := client.HGet(ctx, strconv.Itoa(-1*bucketID), "accessCount").Result()
	if err != nil {
		return 0, err
	}
	accessCount, err := strconv.Atoi(accessCountS)
	if err != nil {
		return 0, err
	}
	return accessCount, nil
}

// ReadBucket reads exactly Z blocks from the bucket.
// It reads all the valid real blocks and random vaid dummy blocks if the bucket contains less than Z valid real blocks.
// blocks is a map of block id to block values.
func (s *StorageHandler) ReadBucket(bucketID int, storageID int) (blocks map[string]string, err error) {
	// TODO: implement
	client := s.getClient(storageID)
	ctx := context.Background()
	blocks = make(map[string]string)
	i := 0
	bit := 0
	for ; i < Z; {
		pos, key, err := s.GetMetadata(bucketID, strconv.Itoa(bit), storageID)
		if err != nil {
			return nil, err
		}
		value, err := client.HGet(ctx, strconv.Itoa(bucketID), strconv.Itoa(pos)).Result()
		if err != nil {
			return nil, err
		}
		if value != "__null__" {
			value, err = Decrypt(value, s.key[storageID])
			blocks[key] = value
			i++
		}
		bit++
	}
	if err != nil {
		return nil, err
	}
	return blocks, nil
}

// WriteBucket writes readBucketBlocks and shardNodeBlocks to the storage shard.
// It priorotizes readBucketBlocks to shardNodeBlocks.
// It returns the blocks that were written into the storage shard in the writtenBlocks variable.
func (s *StorageHandler) WriteBucket(bucketID int, storageID int, readBucketBlocks map[string]string, shardNodeBlocks map[string]string, isAtomic bool) (writtenBlocks map[string]string, err error) {
	// TODO: implement
	// TODO: It should make the counter zero
	values := make([]string, Z+S)
	metadatas := make([]string, Z+S)
	realIndex := make([]int, Z+S)
	for k := 0; k < Z+S; k++ {
		// Generate a random number between 0 and 9
		realIndex[k] = k
	}
	shuffleArray(realIndex)
	writtenBlocks = make(map[string]string)
	i := 0
	for key, value := range readBucketBlocks {
		if len(writtenBlocks) < Z {
			writtenBlocks[key] = value
			values[realIndex[i]] = value
			metadatas[i] = strconv.Itoa(realIndex[i]) + key
			i++
			// pos_map is updated in server? 
		} else {
			break
		}
	}
	for key, value := range shardNodeBlocks {
		if len(writtenBlocks) < Z {
			writtenBlocks[key] = value
			values[realIndex[i]] = value
			metadatas[i] = strconv.Itoa(realIndex[i]) + key
			i++
		} else {
			break
		}
	}
	rand.Seed(time.Now().UnixNano())
	dummyCount := rand.Intn(1000)
	for ; i < Z+S; i++ {
		dummyID := "dummy" + strconv.Itoa(dummyCount)
		dummyString := "b" + strconv.Itoa(bucketID) + "d" + strconv.Itoa(i)
		dummyString, err = Encrypt(dummyString, s.key[storageID])
		if err != nil {
			fmt.Println("Error encrypting data")
			return nil, err
		}
		// push dummy to array
		values[realIndex[i]] = dummyString
		// push meta data of dummies to array
		metadatas[i] = strconv.Itoa(realIndex[i]) + dummyID
		dummyCount++
	}
	// push content of value array and meta data array
	err = s.Push(bucketID, values, storageID)
	if err != nil {
		fmt.Println("Error pushing values to db:", err)
		return nil, err
	}
	err = s.PushMetadata(bucketID, metadatas, storageID)
	if err != nil {
		fmt.Println("Error pushing metadatas to db:", err)
		return nil, err
	}
	return writtenBlocks, nil
}

// ReadBlock reads a single block using an its offset.
func (s *StorageHandler) ReadBlock(bucketID int, storageID int, offset int) (value string, err error) {
	// TODO: it should invalidate and increase counter
	client := s.getClient(storageID)
	ctx := context.Background()
	value, err = client.HGet(ctx, strconv.Itoa(bucketID), strconv.Itoa(offset)).Result()
	if err != nil {
		return "", err
	}
	if value == "__null__" {
		err = errors.New("you are accessing invalidate value")
		return "", err
	}
	// decode value
	value, err = Decrypt(value, s.key[storageID])
	if err != nil {
		return "", err
	}
	// invalidate value (set it to null)
	err = client.HSet(ctx, strconv.Itoa(bucketID), strconv.Itoa(offset), "__null__").Err()
	if err != nil {
		return "", err
	}
	// increment access count
	accessCountS, err := client.HGet(ctx, strconv.Itoa(-1*bucketID), "accessCount").Result()
	if err != nil {
		return "", err
	}
	accessCount, err := strconv.Atoi(accessCountS)
	if err != nil {
		return "", err
	}
	err = client.HSet(ctx, strconv.Itoa(-1*bucketID), "accessCount", accessCount+1).Err()
	if err != nil {
		return "", err
	}
	return value, nil
}

// GetBucketsInPaths return all the bucket ids for the passed paths.
func (s *StorageHandler) GetBucketsInPaths(paths []int) (bucketIDs []int, err error) {
	buckets := make(IntSet)
	for i := 0; i < len(paths); i++ {
		for bucketId := paths[i]; bucketId > 0; bucketId = bucketId >> shift {
			if buckets.Contains(bucketId) {
				break;
			} else {
				buckets.Add(bucketId)
			}
		}
	}
	bucketIDs = make([]int, len(buckets))
	i := 0
	for key := range buckets {
		bucketIDs[i] = key
		i++
	}
	return bucketIDs, nil
}
