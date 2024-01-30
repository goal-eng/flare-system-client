package finalizer

import (
	"bytes"
	"cmp"
	"flare-tlc/client/shared"
	"flare-tlc/utils/contracts/relay"
	"fmt"
	"math/big"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

// Duplicates relay.RelaySigningPolicyInitialized but with fewer fields and
// different types for some fields
type signingPolicy struct {
	rewardEpochId      int64
	startVotingRoundId uint32
	threshold          uint16
	seed               *big.Int
	voters             []common.Address
	weights            []uint16
	rawBytes           []byte
	blockTimestamp     uint64
}

func newSigningPolicy(r *relay.RelaySigningPolicyInitialized) *signingPolicy {
	return &signingPolicy{
		rewardEpochId:      r.RewardEpochId.Int64(),
		startVotingRoundId: r.StartVotingRoundId,
		threshold:          r.Threshold,
		seed:               r.Seed,
		voters:             r.Voters,
		weights:            r.Weights,
		rawBytes:           r.SigningPolicyBytes,
		blockTimestamp:     r.Timestamp,
	}
}

type voterData struct {
	index  int
	weight uint16
}

type signingPolicyStorage struct {

	// sorted list of signing policies, sorted by rewardEpochId (and also by startVotingRoundId)
	spList []*signingPolicy

	// rewardEpochId -> voter -> { index, weight }
	voterMap map[int64]map[common.Address]voterData

	// mutex
	sync.Mutex
}

func newSigningPolicyStorage() *signingPolicyStorage {
	return &signingPolicyStorage{
		spList:   make([]*signingPolicy, 0, 10),
		voterMap: make(map[int64]map[common.Address]voterData),
	}
}

// Does not lock the structure, should be called from a function that does lock.
// We assume that the list is sorted by rewardEpochId and also by startVotingRoundId.
func (s *signingPolicyStorage) findByVotingRoundId(votingRoundId uint32) *signingPolicy {
	i, found := sort.Find(len(s.spList), func(i int) int {
		return cmp.Compare(votingRoundId, s.spList[i].startVotingRoundId)
	})
	if found {
		return s.spList[i]
	}
	if i == 0 {
		return nil
	}
	return s.spList[i-1]
}

func (s *signingPolicyStorage) Add(sp *signingPolicy) error {
	s.Lock()
	defer s.Unlock()

	if len(s.spList) > 0 {
		// check consistency, previous epoch should be already added
		if s.spList[len(s.spList)-1].rewardEpochId != sp.rewardEpochId-1 {
			return fmt.Errorf("missing signing policy for reward epoch id %d", sp.rewardEpochId-1)
		}
		// should be sorted by voting round id, should not happen
		if sp.startVotingRoundId < s.spList[len(s.spList)-1].startVotingRoundId {
			return fmt.Errorf("signing policy for reward epoch id %d has larger start voting round id than previous policy",
				sp.rewardEpochId)
		}
	}

	s.spList = append(s.spList, sp)

	vMap := make(map[common.Address]voterData)
	s.voterMap[sp.rewardEpochId] = vMap
	for i, voter := range sp.voters {
		if _, ok := vMap[voter]; !ok {
			vMap[voter] = voterData{
				index:  i,
				weight: sp.weights[i],
			}
		}
	}
	return nil
}

func (s *signingPolicyStorage) GetForVotingRound(votingRoundId uint32) *signingPolicy {
	s.Lock()
	defer s.Unlock()

	return s.findByVotingRoundId(votingRoundId)
}

func (s *signingPolicyStorage) First() *signingPolicy {
	s.Lock()
	defer s.Unlock()

	if len(s.spList) == 0 {
		return nil
	}
	return s.spList[0]
}

// Removes all signing policies with start voting round id <= than the provided one.
// Returns the list of removed reward epoch ids.
func (s *signingPolicyStorage) RemoveByVotingRound(votingRoundId uint32) []uint32 {
	s.Lock()
	defer s.Unlock()

	var removedRewardEpochIds []uint32
	for len(s.spList) > 0 && s.spList[0].startVotingRoundId <= votingRoundId {
		removedRewardEpochIds = append(removedRewardEpochIds, uint32(s.spList[0].rewardEpochId))
		delete(s.voterMap, s.spList[0].rewardEpochId)
		s.spList[0] = nil
		s.spList = s.spList[1:]
	}
	return removedRewardEpochIds
}

func (s *signingPolicy) Encode() ([]byte, error) {
	buffer := bytes.NewBuffer(nil)

	size := len(s.voters)

	sizeBytes := shared.Uint16toBytes(uint16(size))
	epochBytes := shared.Uint32toBytes(uint32(s.rewardEpochId))
	startVotingRoundBytes := shared.Uint32toBytes(s.startVotingRoundId)
	thresholdBytes := shared.Uint16toBytes(s.threshold)

	buffer.Write(sizeBytes[:])
	buffer.Write(epochBytes[:])
	buffer.Write(startVotingRoundBytes[:])
	buffer.Write(thresholdBytes[:])
	buffer.Write(s.seed.Bytes())

	// voters and weights
	for i := 0; i < size; i++ {
		weightBytes := shared.Uint16toBytes(s.weights[i])
		buffer.Write(s.voters[i].Bytes())
		buffer.Write(weightBytes[:])
	}
	return buffer.Bytes(), nil
}
